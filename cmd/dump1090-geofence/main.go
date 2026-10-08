// Command dump1090-geofence is a region-filter proxy for ADS-B feeds: it
// sits between a dump1090/readsb decoder and the consumers you share the
// feed with, forwarding only the aircraft that pass the configured
// geofence, altitude, flag and address filters.
//
// It speaks Beast upstream and downstream when the upstream is Beast, SBS
// when the upstream is SBS, mirrors the decoder's aircraft.json under the
// same path on its own HTTP endpoint, and can replay a log with --dry-run
// to show what would be forwarded and why.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/technothatch/dump1090-geofence/internal/adsb"
	"github.com/technothatch/dump1090-geofence/internal/beast"
	"github.com/technothatch/dump1090-geofence/internal/config"
	"github.com/technothatch/dump1090-geofence/internal/fence"
	"github.com/technothatch/dump1090-geofence/internal/region"
	"github.com/technothatch/dump1090-geofence/internal/sbs"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// runtime is the fully resolved, validated startup state.
type runtime struct {
	cfg     config.Options
	filter  *fence.Filter
	tracker *fence.Tracker
	dryRun  bool
	dryIn   string
}

// run is main's testable entry point. Exit codes: 0 success (including
// --help), 1 configuration or runtime failure, 2 flag syntax error.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	rt, err := resolve(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "dump1090-geofence: %v\n", err)
		return 1
	}
	if rt.dryRun {
		return runDryRun(rt, stdin, stdout, stderr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(rt, ctx, stderr)
}

// preScan extracts the flags that must be known before the rest of the
// configuration can be loaded (the config file path) and the optional
// --dry-run VALUE argument, which the flag package cannot treat as
// optional. VALUE is an input file path, or the literal "-" meaning
// standard input (the same marker --dry-run=- and bare --dry-run imply).
func preScan(args []string) (cfgPath string, dryRun bool, dryIn string, rest []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "--config="):
			cfgPath = a[len("--config="):]
			continue
		case strings.HasPrefix(a, "-config="):
			cfgPath = a[len("-config="):]
			continue
		case strings.HasPrefix(a, "--dry-run="):
			dryRun, dryIn = true, a[len("--dry-run="):]
			continue
		case strings.HasPrefix(a, "-dry-run="):
			dryRun, dryIn = true, a[len("-dry-run="):]
			continue
		}
		switch a {
		case "--config", "-config":
			if i+1 < len(args) {
				cfgPath = args[i+1]
				i++
			}
		case "--dry-run", "-dry-run":
			dryRun = true
			// Consume the value unless it is another flag. A bare "-"
			// is the stdin marker, not a flag, so it is consumed too.
			if i+1 < len(args) && (args[i+1] == "-" || !strings.HasPrefix(args[i+1], "-")) {
				dryIn = args[i+1]
				i++
			} else {
				dryIn = "-"
			}
		default:
			rest = append(rest, a)
		}
	}
	if dryRun && dryIn == "" {
		dryIn = "-"
	}
	return
}

// hexListFlag collects repeatable, comma-separated hex address flags.
type hexListFlag struct {
	values []string
}

func (h *hexListFlag) String() string { return strings.Join(h.values, ",") }

func (h *hexListFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			h.values = append(h.values, p)
		}
	}
	return nil
}

// resolve merges built-in defaults, environment, config file and flags
// (in that precedence order), validates the result and builds the region,
// policy and tracker.
func resolve(args []string, stderr io.Writer) (*runtime, error) {
	cfgPath, dryRun, dryIn, rest := preScan(args)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}

	fs := flag.NewFlagSet("dump1090-geofence", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: dump1090-geofence [flags]\n\n"+
			"Region-filter proxy for ADS-B feeds: forwards only the aircraft inside\n"+
			"the configured fence. Exactly one of --box/--circle is required.\n\n")
		fs.PrintDefaults()
	}

	upstream := fs.String("upstream", cfg.UpstreamAddr, "decoder feed address (host:port)")
	upstreamType := fs.String("upstream-type", cfg.UpstreamType, "decoder feed format: beast or sbs")
	listen := fs.String("listen", cfg.Listen, "filtered output listen address (format matches --upstream-type)")
	httpListen := fs.String("http-listen", cfg.HTTPListen, "listen address for /data/aircraft.json and /healthz")
	decoderURL := fs.String("url", cfg.DecoderURL, "decoder base URL for position polling (env DUMP1090_URL)")
	pollInterval := fs.Duration("poll-interval", cfg.PollInterval, "aircraft.json poll interval (beast upstream)")
	noPoll := fs.Bool("no-poll", cfg.NoPoll, "disable decoder polling (sbs upstream only; positions come from the stream)")
	box := fs.String("box", cfg.Box, "region: GeoJSON file path or minLat,minLon,maxLat,maxLon")
	circle := fs.String("circle", cfg.Circle, "region: lat,lon,radius_nm")
	above := fs.Float64("above", cfg.Above, "minimum altitude in feet (-Inf disables)")
	below := fs.Float64("below", cfg.Below, "maximum altitude in feet (+Inf disables)")
	var allowHex, denyHex hexListFlag
	fs.Var(&allowHex, "allow-hex", "comma-separated ICAO addresses allowed regardless of other filters (repeatable)")
	fs.Var(&denyHex, "deny-hex", "comma-separated ICAO addresses always denied (repeatable)")
	militaryOnly := fs.Bool("military-only", cfg.MilitaryOnly, "forward only military aircraft (dbFlags & 1)")
	dropOnGround := fs.Bool("drop-on-ground", cfg.DropOnGround, "drop aircraft reported on the ground")
	linger := fs.Duration("linger", cfg.Linger, "keep an allowed aircraft this long after its last accepted message")
	fs.Bool("dry-run", dryRun, "replay an SBS log or aircraft.json and print decisions instead of serving (input: file path, or '-' for stdin)")

	if err := fs.Parse(rest); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q (all inputs are flags)", fs.Arg(0))
	}

	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })

	// Either region flag on the command line makes the CLI the single
	// source of truth for the region; the untouched one is cleared.
	if visited["box"] || visited["circle"] {
		if visited["box"] {
			cfg.Box = *box
		} else {
			cfg.Box = ""
		}
		if visited["circle"] {
			cfg.Circle = *circle
		} else {
			cfg.Circle = ""
		}
	}
	// Slice flags replace (never merge with) the configured lists.
	if visited["allow-hex"] {
		cfg.AllowHex = allowHex.values
	}
	if visited["deny-hex"] {
		cfg.DenyHex = denyHex.values
	}

	cfg.UpstreamAddr = *upstream
	cfg.UpstreamType = *upstreamType
	cfg.Listen = *listen
	cfg.HTTPListen = *httpListen
	cfg.DecoderURL = strings.TrimRight(*decoderURL, "/")
	cfg.PollInterval = *pollInterval
	cfg.NoPoll = *noPoll
	cfg.Above = *above
	cfg.Below = *below
	cfg.MilitaryOnly = *militaryOnly
	cfg.DropOnGround = *dropOnGround
	cfg.Linger = *linger

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	reg, err := region.Build(cfg.Box, cfg.Circle)
	if err != nil {
		return nil, err
	}
	filter := fence.NewFilter(reg, cfg)
	return &runtime{
		cfg:     cfg,
		filter:  filter,
		tracker: fence.NewTracker(filter),
		dryRun:  dryRun,
		dryIn:   dryIn,
	}, nil
}

// runDryRun replays input through the real filter and prints one
// decision line per record, sorted by hex, plus a summary. Input is SBS
// lines, or an aircraft.json document when it starts with '{'.
func runDryRun(rt *runtime, stdin io.Reader, stdout, stderr io.Writer) int {
	r := stdin
	if rt.dryIn != "-" {
		f, err := os.Open(rt.dryIn)
		if err != nil {
			fmt.Fprintf(stderr, "dump1090-geofence: %v\n", err)
			return 1
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(r)
	if err != nil {
		fmt.Fprintf(stderr, "dump1090-geofence: read input: %v\n", err)
		return 1
	}

	var verdicts []fence.Verdict
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '{' {
		doc, err := adsb.ParseDoc(data)
		if err != nil {
			fmt.Fprintf(stderr, "dump1090-geofence: %v\n", err)
			return 1
		}
		for i := range doc.Aircraft {
			verdicts = append(verdicts, rt.tracker.Observe(&doc.Aircraft[i]))
		}
		rt.tracker.SetMessages(doc.Messages)
	} else {
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Bytes()
			if sbs.IsHeartbeat(line) {
				continue
			}
			obs, ok := sbs.Parse(line)
			if !ok {
				fmt.Fprintf(stderr, "dump1090-geofence: line %d: not a usable MSG record, skipped\n", lineNo)
				continue
			}
			verdicts = append(verdicts, rt.tracker.Observe(obs))
		}
		if err := sc.Err(); err != nil {
			fmt.Fprintf(stderr, "dump1090-geofence: read input: %v\n", err)
			return 1
		}
	}

	sort.SliceStable(verdicts, func(i, j int) bool { return verdicts[i].Hex < verdicts[j].Hex })
	allowed := 0
	for _, v := range verdicts {
		if v.Allow {
			allowed++
		}
		fmt.Fprintln(stdout, fence.FormatDecision(v))
	}
	fmt.Fprintf(stdout, "checked=%d allowed=%d\n", len(verdicts), allowed)
	return 0
}

// serve runs the proxy until the process is signalled: HTTP status
// endpoint, filtered listener, upstream loop and (unless --no-poll) the
// decoder poll loop.
func serve(rt *runtime, ctx context.Context, stderr io.Writer) int {
	cfg := rt.cfg
	bc := newBroadcaster()

	mux := http.NewServeMux()
	mux.HandleFunc("/data/aircraft.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rt.tracker.Snapshot())
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	srv := &http.Server{Addr: cfg.HTTPListen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	httpErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			httpErr <- err
		}
	}()

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		fmt.Fprintf(stderr, "dump1090-geofence: listen %s: %v\n", cfg.Listen, err)
		return 1
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
		bc.CloseAll()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	go acceptLoop(ln, cfg.UpstreamType, bc)
	go upstreamLoop(ctx, cfg, rt.tracker, bc, stderr)
	if !cfg.NoPoll {
		go pollLoop(ctx, cfg, rt.tracker, stderr)
	}
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				rt.tracker.Prune()
			}
		}
	}()

	fmt.Fprintf(stderr, "dump1090-geofence: region %s\n", rt.filter.Region().Describe())
	fmt.Fprintf(stderr, "dump1090-geofence: listening on %s (%s), http on %s, upstream %s %s\n",
		cfg.Listen, cfg.UpstreamType, cfg.HTTPListen, cfg.UpstreamType, cfg.UpstreamAddr)
	if !cfg.NoPoll {
		fmt.Fprintf(stderr, "dump1090-geofence: polling %s/data/aircraft.json every %s\n",
			cfg.DecoderURL, cfg.PollInterval)
	}

	select {
	case <-ctx.Done():
		return 0
	case err := <-httpErr:
		fmt.Fprintf(stderr, "dump1090-geofence: http %s: %v\n", cfg.HTTPListen, err)
		return 1
	}
}

// acceptLoop hands accepted clients to per-connection handlers; the
// client-facing format always mirrors the upstream format.
func acceptLoop(ln net.Listener, upstreamType string, bc *broadcaster) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed at shutdown
		}
		if upstreamType == config.UpstreamBeast {
			go serveBeastClient(conn, bc)
		} else {
			go serveSBSClient(conn, bc)
		}
	}
}

// serveBeastClient registers the connection for broadcasts and drains the
// client's command frames. Commands are consumed but deliberately not
// forwarded upstream: they reconfigure a single dump1090 connection and
// applying one consumer's request to the shared upstream link would
// change the feed for everybody.
func serveBeastClient(conn net.Conn, bc *broadcaster) {
	bc.Add(conn)
	defer func() {
		bc.Remove(conn)
		_ = conn.Close()
	}()
	cr := beast.NewCommandReader(conn)
	for {
		if _, err := cr.Next(); err != nil {
			return
		}
	}
}

// serveSBSClient registers the connection and discards anything the
// client sends (SBS consumers are read-only).
func serveSBSClient(conn net.Conn, bc *broadcaster) {
	bc.Add(conn)
	defer func() {
		bc.Remove(conn)
		_ = conn.Close()
	}()
	_, _ = io.Copy(io.Discard, conn)
}

// upstreamLoop keeps (re)connecting to the decoder and feeding the
// filter. Read errors are logged and retried with a short backoff.
func upstreamLoop(ctx context.Context, cfg config.Options, tr *fence.Tracker, bc *broadcaster, stderr io.Writer) {
	const backoff = time.Second
	for ctx.Err() == nil {
		conn, err := net.DialTimeout("tcp", cfg.UpstreamAddr, 5*time.Second)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue
		}
		var readErr error
		if cfg.UpstreamType == config.UpstreamBeast {
			readErr = readBeast(ctx, conn, tr, bc)
		} else {
			readErr = readSBS(ctx, conn, tr, bc)
		}
		_ = conn.Close()
		if ctx.Err() != nil {
			return
		}
		fmt.Fprintf(stderr, "dump1090-geofence: upstream %s closed: %v (retrying)\n", cfg.UpstreamAddr, readErr)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// readBeast filters the Beast stream until the connection fails:
// heartbeats pass untouched, Mode A/C and Radarcape position frames are
// dropped, and Mode S frames pass only when the tracker currently vouches
// for their address.
func readBeast(ctx context.Context, conn net.Conn, tr *fence.Tracker, bc *broadcaster) error {
	// Unblock the reader at shutdown.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	rd := beast.NewReader(conn)
	for {
		f, err := rd.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		switch {
		case f.IsHeartbeat():
			bc.Send(f.Raw)
		case f.IsModeAC(), f.Type == beast.TypeRadar:
			// No ICAO address (Mode A/C) or receiver position data
			// (Radarcape '5'): never forwarded.
		default:
			hex, ok := beast.ExtractICAO(f.Payload)
			if !ok {
				continue
			}
			tr.CountMessage(hex)
			if tr.CanForward(hex) {
				bc.Send(f.Raw)
			}
		}
	}
}

// readSBS filters the SBS stream: empty-line heartbeats pass, every other
// line is parsed into a partial observation and forwarded verbatim only
// when the merged verdict allows it.
func readSBS(ctx context.Context, conn net.Conn, tr *fence.Tracker, bc *broadcaster) error {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if sbs.IsHeartbeat(line) {
			bc.Send([]byte(sbs.Heartbeat))
			continue
		}
		obs, ok := sbs.Parse(line)
		if !ok {
			continue // unparseable lines carry no trustworthy address
		}
		v := tr.Observe(obs)
		if v.Allow {
			out := make([]byte, 0, len(line)+2)
			out = append(out, line...)
			out = append(out, "\r\n"...)
			bc.Send(out)
		}
	}
	return sc.Err()
}

// pollLoop keeps the tracker fed with positions (and dbFlags) from the
// decoder's aircraft.json — the only position source for Beast upstreams.
func pollLoop(ctx context.Context, cfg config.Options, tr *fence.Tracker, stderr io.Writer) {
	client := &http.Client{Timeout: 5 * time.Second}
	url := strings.TrimRight(cfg.DecoderURL, "/") + "/data/aircraft.json"

	pollOnce := func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			fmt.Fprintf(stderr, "dump1090-geofence: poll %s: %v\n", url, err)
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintf(stderr, "dump1090-geofence: poll %s: %v\n", url, err)
			}
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			fmt.Fprintf(stderr, "dump1090-geofence: poll %s: %s\n", url, resp.Status)
			return
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			fmt.Fprintf(stderr, "dump1090-geofence: poll %s: %v\n", url, err)
			return
		}
		doc, err := adsb.ParseDoc(data)
		if err != nil {
			fmt.Fprintf(stderr, "dump1090-geofence: poll %s: %v\n", url, err)
			return
		}
		for i := range doc.Aircraft {
			tr.Observe(&doc.Aircraft[i])
		}
		tr.SetMessages(doc.Messages)
	}

	pollOnce()
	t := time.NewTicker(cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pollOnce()
		}
	}
}

// broadcaster fans one framed message out to every connected consumer,
// evicting clients that cannot keep up.
type broadcaster struct {
	mu      sync.Mutex
	clients map[net.Conn]struct{}
}

func newBroadcaster() *broadcaster {
	return &broadcaster{clients: make(map[net.Conn]struct{})}
}

func (b *broadcaster) Add(c net.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clients[c] = struct{}{}
}

func (b *broadcaster) Remove(c net.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.clients, c)
}

// Send writes data to every client. A client that errors or stalls past
// the write deadline is closed and dropped rather than blocking the feed.
func (b *broadcaster) Send(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.clients {
		_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write(data); err != nil {
			_ = c.Close()
			delete(b.clients, c)
		}
	}
}

// CloseAll closes every client connection (shutdown).
func (b *broadcaster) CloseAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.clients {
		_ = c.Close()
		delete(b.clients, c)
	}
}

// count reports the live client count (tests).
func (b *broadcaster) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.clients)
}
