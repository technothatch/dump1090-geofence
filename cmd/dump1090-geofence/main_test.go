package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/technothatch/dump1090-geofence/internal/adsb"
)

// --- resolve: precedence, validation -------------------------------------

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fence.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func mustResolve(t *testing.T, args ...string) *runtime {
	t.Helper()
	var out bytes.Buffer
	rt, err := resolve(args, &out)
	if err != nil {
		t.Fatalf("resolve(%v): %v\n%s", args, err, out.String())
	}
	return rt
}

func resolveErr(t *testing.T, args ...string) error {
	t.Helper()
	_, err := resolve(args, io.Discard)
	if err == nil {
		t.Fatalf("resolve(%v) succeeded, want error", args)
	}
	return err
}

func TestResolveDefaults(t *testing.T) {
	rt := mustResolve(t, "--box", "50,-5,53,2")
	cfg := rt.cfg
	if cfg.UpstreamType != "beast" || cfg.UpstreamAddr != "localhost:30005" {
		t.Errorf("upstream = %s %s", cfg.UpstreamType, cfg.UpstreamAddr)
	}
	if cfg.Listen != ":30006" || cfg.HTTPListen != ":8081" {
		t.Errorf("listen = %s / %s", cfg.Listen, cfg.HTTPListen)
	}
	if cfg.Linger != 30*time.Second || cfg.PollInterval != time.Second {
		t.Errorf("linger = %s poll = %s", cfg.Linger, cfg.PollInterval)
	}
	if cfg.NoPoll {
		t.Error("NoPoll default must be false")
	}
	if rt.filter == nil || rt.tracker == nil {
		t.Fatal("policy/tracker not built")
	}
	if rt.dryRun {
		t.Error("dry-run enabled without flag")
	}
	if got := rt.filter.Region().Describe(); !strings.HasPrefix(got, "box(") {
		t.Errorf("region = %q", got)
	}
}

func TestResolveConfigFileValues(t *testing.T) {
	path := writeConfig(t, `
[upstream]
type = "sbs"
addr = "10.0.0.5:30003"
[output]
listen = ":30011"
[region]
box = "51.4,-0.6,51.6,-0.2"
[filters]
allow_hex = ["3C673A"]
linger = 45
`)
	rt := mustResolve(t, "--config", path)
	cfg := rt.cfg
	if cfg.UpstreamType != "sbs" || cfg.UpstreamAddr != "10.0.0.5:30003" {
		t.Errorf("upstream = %s %s", cfg.UpstreamType, cfg.UpstreamAddr)
	}
	if cfg.Listen != ":30011" {
		t.Errorf("listen = %s", cfg.Listen)
	}
	if cfg.Box != "51.4,-0.6,51.6,-0.2" {
		t.Errorf("box = %q", cfg.Box)
	}
	if !reflect.DeepEqual(cfg.AllowHex, []string{"3c673a"}) {
		t.Errorf("allow = %v", cfg.AllowHex)
	}
	if cfg.Linger != 45*time.Second {
		t.Errorf("linger = %s", cfg.Linger)
	}
}

func TestResolveRegionPrecedence(t *testing.T) {
	path := writeConfig(t, "[region]\nbox = \"50,-5,53,2\"\n")

	t.Run("config region used when no region flag", func(t *testing.T) {
		rt := mustResolve(t, "--config", path)
		if rt.cfg.Box == "" || rt.cfg.Circle != "" {
			t.Errorf("box=%q circle=%q", rt.cfg.Box, rt.cfg.Circle)
		}
	})

	t.Run("circle flag clears configured box", func(t *testing.T) {
		rt := mustResolve(t, "--config", path, "--circle", "51.5,-0.1,40")
		if rt.cfg.Circle != "51.5,-0.1,40" || rt.cfg.Box != "" {
			t.Errorf("box=%q circle=%q (CLI must win entirely)", rt.cfg.Box, rt.cfg.Circle)
		}
	})

	t.Run("box flag wins over configured circle", func(t *testing.T) {
		cpath := writeConfig(t, "[region]\ncircle = \"51.5,-0.1,10\"\n")
		rt := mustResolve(t, "--config", cpath, "--box", "50,-5,53,2")
		if rt.cfg.Box != "50,-5,53,2" || rt.cfg.Circle != "" {
			t.Errorf("box=%q circle=%q", rt.cfg.Box, rt.cfg.Circle)
		}
	})
}

func TestResolvePrecedenceFlagOverFileOverEnv(t *testing.T) {
	t.Setenv("DUMP1090_URL", "http://env-host:9090/")
	path := writeConfig(t, "[decoder]\nurl = \"http://file-host:8080\"\n")

	t.Run("env over built-in", func(t *testing.T) {
		rt := mustResolve(t, "--box", "50,-5,53,2")
		if rt.cfg.DecoderURL != "http://env-host:9090" {
			t.Errorf("url = %q", rt.cfg.DecoderURL)
		}
	})
	t.Run("config file over env", func(t *testing.T) {
		rt := mustResolve(t, "--config", path, "--box", "50,-5,53,2")
		if rt.cfg.DecoderURL != "http://file-host:8080" {
			t.Errorf("url = %q", rt.cfg.DecoderURL)
		}
	})
	t.Run("flag over config file", func(t *testing.T) {
		rt := mustResolve(t, "--config", path, "--box", "50,-5,53,2", "--url", "http://flag-host:1/")
		if rt.cfg.DecoderURL != "http://flag-host:1" {
			t.Errorf("url = %q", rt.cfg.DecoderURL)
		}
	})
}

func TestResolveHexListFlagsReplaceConfig(t *testing.T) {
	path := writeConfig(t, "[filters]\nallow_hex = [\"aabbcc\"]\ndeny_hex = [\"deadbe\"]\n")

	t.Run("flag replaces config list", func(t *testing.T) {
		rt := mustResolve(t, "--config", path, "--box", "50,-5,53,2", "--allow-hex", "DDEEFF,112233")
		if !reflect.DeepEqual(rt.cfg.AllowHex, []string{"DDEEFF", "112233"}) {
			t.Errorf("allow = %v, want flag values only", rt.cfg.AllowHex)
		}
		if !reflect.DeepEqual(rt.cfg.DenyHex, []string{"deadbe"}) {
			t.Errorf("deny = %v, untouched list must survive", rt.cfg.DenyHex)
		}
	})

	t.Run("repeated flag appends", func(t *testing.T) {
		rt := mustResolve(t, "--box", "50,-5,53,2",
			"--allow-hex", "aa11bb", "--allow-hex", "cc22dd")
		if !reflect.DeepEqual(rt.cfg.AllowHex, []string{"aa11bb", "cc22dd"}) {
			t.Errorf("allow = %v", rt.cfg.AllowHex)
		}
	})

	t.Run("config list kept without flag", func(t *testing.T) {
		rt := mustResolve(t, "--config", path, "--box", "50,-5,53,2")
		if !reflect.DeepEqual(rt.cfg.AllowHex, []string{"aabbcc"}) {
			t.Errorf("allow = %v", rt.cfg.AllowHex)
		}
	})
}

func TestResolveValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantSub string
	}{
		{name: "no region", args: nil, wantSub: "exactly one region"},
		{
			name:    "both regions",
			args:    []string{"--box", "50,-5,53,2", "--circle", "51.5,-0.1,10"},
			wantSub: "exactly one region",
		},
		{
			name:    "bad upstream type",
			args:    []string{"--box", "50,-5,53,2", "--upstream-type", "avr"},
			wantSub: "--upstream-type",
		},
		{
			name:    "upstream without port",
			args:    []string{"--box", "50,-5,53,2", "--upstream", "localhost"},
			wantSub: "host:port",
		},
		{
			name:    "no-poll with beast",
			args:    []string{"--box", "50,-5,53,2", "--no-poll"},
			wantSub: "--no-poll is only valid",
		},
		{
			name:    "inverted altitude band",
			args:    []string{"--box", "50,-5,53,2", "--above", "20000", "--below", "1000"},
			wantSub: "--above",
		},
		{
			name:    "invalid allow hex",
			args:    []string{"--box", "50,-5,53,2", "--allow-hex", "nothex"},
			wantSub: "--allow-hex",
		},
		{
			name:    "missing config file",
			args:    []string{"--config", filepath.Join(t.TempDir(), "nope.toml"), "--box", "50,-5,53,2"},
			wantSub: "read config",
		},
		{
			name:    "bad box bounds",
			args:    []string{"--box", "99,0,100,1"},
			wantSub: "out of range",
		},
		{
			name:    "bad circle",
			args:    []string{"--circle", "here,there,40"},
			wantSub: "circle component",
		},
		{
			name:    "missing geojson file",
			args:    []string{"--box", filepath.Join(t.TempDir(), "nope.geojson")},
			wantSub: "read geojson",
		},
		{
			name:    "unknown flag",
			args:    []string{"--bogus", "--box", "50,-5,53,2"},
			wantSub: "not defined",
		},
		{
			name:    "unexpected positional argument",
			args:    []string{"stray", "--box", "50,-5,53,2"},
			wantSub: "unexpected argument",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := resolveErr(t, tt.args...)
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want substring %q", err, tt.wantSub)
			}
		})
	}
}

func TestResolveHelp(t *testing.T) {
	var out bytes.Buffer
	_, err := resolve([]string{"-h"}, &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("err = %v, want flag.ErrHelp", err)
	}
	if !strings.Contains(out.String(), "usage: dump1090-geofence") {
		t.Errorf("help text missing: %q", out.String())
	}
}

func TestPreScan(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		cfgPath string
		dryRun  bool
		dryIn   string
		rest    []string
	}{
		{
			name:   "bare dry-run reads stdin",
			args:   []string{"--dry-run", "--box", "50,-5,53,2"},
			dryRun: true,
			dryIn:  "-",
			rest:   []string{"--box", "50,-5,53,2"},
		},
		{
			name:   "dry-run with file",
			args:   []string{"--dry-run", "log.txt", "--box", "50,-5,53,2"},
			dryRun: true,
			dryIn:  "log.txt",
			rest:   []string{"--box", "50,-5,53,2"},
		},
		{
			name:   "dry-run explicit stdin marker is consumed",
			args:   []string{"--dry-run", "-", "--box", "50,-5,53,2"},
			dryRun: true,
			dryIn:  "-",
			rest:   []string{"--box", "50,-5,53,2"},
		},
		{
			name:   "dry-run equals form",
			args:   []string{"--dry-run=-"},
			dryRun: true,
			dryIn:  "-",
		},
		{
			name:    "config path both dash styles",
			args:    []string{"-config=a.toml", "--config", "b.toml"},
			cfgPath: "b.toml",
		},
		{
			name: "nothing special",
			args: []string{"--box", "1,2,3,4"},
			rest: []string{"--box", "1,2,3,4"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath, dryRun, dryIn, rest := preScan(tt.args)
			if cfgPath != tt.cfgPath || dryRun != tt.dryRun || dryIn != tt.dryIn {
				t.Errorf("preScan = (%q, %v, %q), want (%q, %v, %q)",
					cfgPath, dryRun, dryIn, tt.cfgPath, tt.dryRun, tt.dryIn)
			}
			if !reflect.DeepEqual(rest, tt.rest) {
				t.Errorf("rest = %v, want %v", rest, tt.rest)
			}
		})
	}
}

// --- dry-run --------------------------------------------------------------

// msgLine assembles a 22-field SBS MSG line (args are fields 1..21).
func msgLine(fields ...string) string {
	return "MSG," + strings.Join(fields, ",") + "\r\n"
}

func sbsPositionLine(hex, flight, lat, lon, alt string) string {
	return msgLine(
		"3", "1", "1", hex, "1",
		"2026/10/06", "12:00:00.000", "2026/10/06", "12:00:00.000",
		flight, alt, "400", "270.5",
		lat, lon, "0", "7000",
		"0", "0", "0", "0",
	)
}

func TestDryRunSBSLines(t *testing.T) {
	input := sbsPositionLine("3C673A", "BAW123", "51.47000", "-0.45000", "12000") +
		sbsPositionLine("ABCDEF", "ZZZ999", "60.00000", "10.00000", "9000") +
		"this is not an sbs record\r\n" +
		"\r\n" // heartbeat: skipped without counting

	var stdout, stderr bytes.Buffer
	code := run([]string{"--dry-run", "-", "--box", "50,-5,53,2"},
		strings.NewReader(input), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	want := strings.Join([]string{
		"hex=3c673a allow=true reason=inside-region alt=12000 pos=51.47000,-0.45000",
		"hex=abcdef allow=false reason=outside-region alt=9000 pos=60.00000,10.00000",
		"checked=2 allowed=1",
		"",
	}, "\n")
	if stdout.String() != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout.String(), want)
	}
	if !strings.Contains(stderr.String(), "line 3") {
		t.Errorf("stderr = %q, want a warning for line 3", stderr.String())
	}
}

func TestDryRunCountsRepeatedHexSeparately(t *testing.T) {
	input := sbsPositionLine("3C673A", "BAW123", "51.47000", "-0.45000", "12000") +
		sbsPositionLine("3C673A", "BAW123", "51.47100", "-0.45100", "12500")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--dry-run", "-", "--box", "50,-5,53,2"},
		strings.NewReader(input), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout lines = %d (%q)", len(lines), stdout.String())
	}
	if lines[0] != "hex=3c673a allow=true reason=inside-region alt=12000 pos=51.47000,-0.45000" {
		t.Errorf("line 1 = %q", lines[0])
	}
	if lines[2] != "checked=2 allowed=2" {
		t.Errorf("summary = %q", lines[2])
	}
}

func TestDryRunJSONDocument(t *testing.T) {
	doc := `{"now":1759742400,"messages":4242,"aircraft":[
		{"hex":"abcdef","lat":60.0,"lon":10.0,"alt_baro":9000,"flight":"OUT1"},
		{"hex":"3c673a","lat":51.47,"lon":-0.45,"alt_baro":12000,"flight":"BAW123"}
	]}`
	var stdout, stderr bytes.Buffer
	code := run([]string{"--dry-run", "-", "--box", "50,-5,53,2"},
		strings.NewReader(doc), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	want := strings.Join([]string{
		"hex=3c673a allow=true reason=inside-region alt=12000 pos=51.47000,-0.45000",
		"hex=abcdef allow=false reason=outside-region alt=9000 pos=60.00000,10.00000",
		"checked=2 allowed=1",
		"",
	}, "\n")
	if stdout.String() != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout.String(), want)
	}
}

func TestDryRunGroundAircraftJSON(t *testing.T) {
	doc := `{"now":1,"messages":1,"aircraft":[
		{"hex":"3c673a","lat":51.47,"lon":-0.45,"alt_baro":"ground"}
	]}`
	var stdout, stderr bytes.Buffer
	code := run([]string{"--dry-run", "-", "--box", "50,-5,53,2", "--drop-on-ground"},
		strings.NewReader(doc), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	want := "hex=3c673a allow=false reason=on-ground alt=ground pos=51.47000,-0.45000\nchecked=1 allowed=0\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestDryRunReadsFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.sbs")
	body := sbsPositionLine("3C673A", "BAW123", "51.47000", "-0.45000", "12000")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--dry-run", path, "--box", "50,-5,53,2"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "hex=3c673a allow=true") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestDryRunBadJSONFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--dry-run", "-", "--box", "50,-5,53,2"},
		strings.NewReader(`{"aircraft":[{"hex":"zz"}]}`), &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "hex") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestDryRunMissingFileFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--dry-run", filepath.Join(t.TempDir(), "nope.log"), "--box", "50,-5,53,2"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

func TestDryRunAllowHexBypassesRegion(t *testing.T) {
	input := sbsPositionLine("3C673A", "BAW123", "60.00000", "10.00000", "9000")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--dry-run", "-", "--box", "50,-5,53,2", "--allow-hex", "3c673a",
	}, strings.NewReader(input), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	want := "hex=3c673a allow=true reason=allow-hex alt=9000 pos=60.00000,10.00000\nchecked=1 allowed=1\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

// --- run: exit codes ------------------------------------------------------

func TestRunHelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-h"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "usage: dump1090-geofence") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunConfigErrorExitsOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{}, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "exactly one region") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// --- broadcaster ----------------------------------------------------------

func tcpPair(t *testing.T) (server, client net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := ln.Accept()
		ch <- res{c, err}
	}()
	d, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatalf("accept: %v", r.err)
	}
	t.Cleanup(func() {
		_ = d.Close()
		_ = r.c.Close()
	})
	return r.c, d
}

func TestBroadcasterDeliversToClients(t *testing.T) {
	b := newBroadcaster()
	srv1, cli1 := tcpPair(t)
	srv2, cli2 := tcpPair(t)
	b.Add(srv1)
	b.Add(srv2)
	if b.count() != 2 {
		t.Fatalf("count = %d", b.count())
	}

	msg := []byte{0x1a, '3', 0xAA, 0xBB}
	for _, cli := range []net.Conn{cli1, cli2} {
		got := make(chan []byte, 1)
		go func(c net.Conn) {
			buf := make([]byte, len(msg))
			_, err := io.ReadFull(c, buf)
			if err != nil {
				got <- nil
				return
			}
			got <- buf
		}(cli)
		b.Send(msg)
		select {
		case g := <-got:
			if !bytes.Equal(g, msg) {
				t.Errorf("client received %x, want %x", g, msg)
			}
		case <-time.After(3 * time.Second):
			t.Error("client did not receive the message")
		}
	}
}

func TestBroadcasterEvictsClosedClients(t *testing.T) {
	b := newBroadcaster()
	srv, _ := tcpPair(t)
	b.Add(srv)
	_ = srv.Close()
	b.Send([]byte("anything"))
	if b.count() != 0 {
		t.Errorf("count = %d, want 0 after send to closed client", b.count())
	}
}

func TestBroadcasterAddRemoveCloseAll(t *testing.T) {
	b := newBroadcaster()
	srv1, _ := tcpPair(t)
	srv2, _ := tcpPair(t)
	b.Add(srv1)
	b.Add(srv2)
	b.Remove(srv1)
	if b.count() != 1 {
		t.Errorf("count = %d, want 1", b.count())
	}
	b.CloseAll()
	if b.count() != 0 {
		t.Errorf("count = %d after CloseAll", b.count())
	}
	// Closed conns must be writable-safe to drop again.
	b.Remove(srv1)
	b.CloseAll()
}

// --- misc -----------------------------------------------------------------

func TestResolveDryRunFlagParsing(t *testing.T) {
	rt := mustResolve(t, "--dry-run", "--box", "50,-5,53,2")
	if !rt.dryRun || rt.dryIn != "-" {
		t.Errorf("dryRun=%v dryIn=%q", rt.dryRun, rt.dryIn)
	}
	rt = mustResolve(t, "--dry-run=input.log", "--box", "50,-5,53,2")
	if !rt.dryRun || rt.dryIn != "input.log" {
		t.Errorf("dryRun=%v dryIn=%q", rt.dryRun, rt.dryIn)
	}
}

func TestServeRejectsDuplicateListenAddrsViaValidate(t *testing.T) {
	// --listen == --http-listen must be caught before anything binds.
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--dry-run", "-",
		"--box", "50,-5,53,2",
		"--listen", ":8081", "--http-listen", ":8081",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "must differ") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestSnapshotJSONShape(t *testing.T) {
	// The HTTP handler serializes tracker.Snapshot(); this pins the JSON
	// contract without binding a port.
	rt := mustResolve(t, "--box", "50,-5,53,2")
	a := &adsb.Aircraft{Hex: "3c673a", Flight: "BAW123"}
	lat, lon := 51.47, -0.45
	a.Lat, a.Lon = &lat, &lon
	a.SetAltBaroFeet(12000)
	if v := rt.tracker.Observe(a); !v.Allow {
		t.Fatalf("Observe denied: %+v", v)
	}
	rt.tracker.SetMessages(7)

	snap := rt.tracker.Snapshot()
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var decoded struct {
		Now      float64          `json:"now"`
		Messages int              `json:"messages"`
		Aircraft []map[string]any `json:"aircraft"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if decoded.Messages != 7 {
		t.Errorf("messages = %d, want 7", decoded.Messages)
	}
	if decoded.Now == 0 {
		t.Error("now missing")
	}
	if len(decoded.Aircraft) != 1 {
		t.Fatalf("aircraft = %d, want 1", len(decoded.Aircraft))
	}
	m := decoded.Aircraft[0]
	for _, key := range []string{"hex", "flight", "lat", "lon", "alt_baro", "seen", "seen_pos", "messages"} {
		if _, ok := m[key]; !ok {
			t.Errorf("aircraft JSON missing %q: %v", key, m)
		}
	}
}
