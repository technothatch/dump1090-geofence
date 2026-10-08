package config

import (
	"fmt"
	"math"
	"net"
	"os"
	"strings"
	"time"

	"github.com/technothatch/dump1090-geofence/internal/adsb"
)

// Environment and built-in defaults.
const (
	// EnvURL overrides the built-in decoder URL when fence.toml does not.
	EnvURL = "DUMP1090_URL"
	// DefaultDecoderURL is the dump1090-fa / readsb default HTTP server.
	DefaultDecoderURL = "http://localhost:8080"
	// DefaultUpstream is dump1090's Beast output port on localhost.
	DefaultUpstream = "localhost:30005"
	// DefaultListen is the filtered output port. 30006 sits just above the
	// dump1090 port block (30001-30005) so the proxy can run next to a
	// decoder on the same host without colliding with it.
	DefaultListen = ":30006"
	// DefaultHTTPListen serves /data/aircraft.json; 8081 leaves the
	// decoder's web UI on 8080 alone.
	DefaultHTTPListen = ":8081"
)

// Upstream transport types.
const (
	UpstreamBeast = "beast"
	UpstreamSBS   = "sbs"
)

// Options is the fully merged runtime configuration: built-in defaults,
// then the DUMP1090_URL environment variable, then fence.toml, then any
// command line flags (the caller overlays flags last).
type Options struct {
	UpstreamAddr string
	UpstreamType string

	Listen     string // filtered Beast or SBS output, matching UpstreamType
	HTTPListen string // /data/aircraft.json

	DecoderURL   string
	PollInterval time.Duration
	NoPoll       bool

	Box    string // GeoJSON file path or "minLat,minLon,maxLat,maxLon"
	Circle string // "lat,lon,radius_nm"

	Above        float64 // feet, -Inf when unconstrained
	Below        float64 // feet, +Inf when unconstrained
	AllowHex     []string
	DenyHex      []string
	MilitaryOnly bool
	DropOnGround bool
	Linger       time.Duration
}

// Defaults returns the built-in options with the environment overlay
// applied. fence.toml and flags are layered on top by the caller.
func Defaults() Options {
	url := DefaultDecoderURL
	if v := strings.TrimSpace(os.Getenv(EnvURL)); v != "" {
		url = strings.TrimRight(v, "/")
	}
	return Options{
		UpstreamAddr: DefaultUpstream,
		UpstreamType: UpstreamBeast,
		Listen:       DefaultListen,
		HTTPListen:   DefaultHTTPListen,
		DecoderURL:   url,
		PollInterval: time.Second,
		Above:        math.Inf(-1),
		Below:        math.Inf(+1),
		Linger:       30 * time.Second,
	}
}

// Load parses path (if non-empty) and overlays every key it sets onto the
// defaults. Unknown sections and keys are errors: in a privacy tool a
// typo'd key must fail loudly rather than silently disable a restriction.
func Load(path string) (Options, error) {
	opts := Defaults()
	if path == "" {
		return opts, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return opts, fmt.Errorf("read config: %w", err)
	}
	doc, err := Parse(data)
	if err != nil {
		return opts, fmt.Errorf("%s: %w", path, err)
	}
	if err := overlay(&opts, doc); err != nil {
		return opts, fmt.Errorf("%s: %w", path, err)
	}
	return opts, nil
}

// overlay applies a parsed document onto opts with strict key checking.
func overlay(opts *Options, doc Doc) error {
	known := map[string]map[string]bool{
		"": {},
		"upstream": {
			"addr": true, "type": true,
		},
		"output": {
			"listen": true, "http_listen": true,
		},
		"decoder": {
			"url": true, "poll_interval": true, "no_poll": true,
		},
		"region": {
			"box": true, "circle": true,
		},
		"filters": {
			"above_ft": true, "below_ft": true, "allow_hex": true,
			"deny_hex": true, "military_only": true, "drop_on_ground": true,
			"linger": true,
		},
	}
	for section, keys := range doc {
		sections, ok := known[section]
		if !ok {
			return fmt.Errorf("unknown section [%s]", section)
		}
		for key := range keys {
			if !sections[key] {
				return fmt.Errorf("unknown key %q in [%s]", key, section)
			}
		}
	}

	type setFunc func(v any) error
	str := func(dst *string) setFunc {
		return func(v any) error {
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("expected a string, got %T", v)
			}
			*dst = s
			return nil
		}
	}
	boolean := func(dst *bool) setFunc {
		return func(v any) error {
			b, ok := v.(bool)
			if !ok {
				return fmt.Errorf("expected true/false, got %T", v)
			}
			*dst = b
			return nil
		}
	}
	number := func(dst *float64) setFunc {
		return func(v any) error {
			switch n := v.(type) {
			case int64:
				*dst = float64(n)
			case float64:
				*dst = n
			default:
				return fmt.Errorf("expected a number, got %T", v)
			}
			return nil
		}
	}
	// duration accepts either a number of seconds or a duration string
	// such as "30s" or "1m30s".
	duration := func(dst *time.Duration) setFunc {
		return func(v any) error {
			switch n := v.(type) {
			case int64:
				*dst = time.Duration(n) * time.Second
				return nil
			case float64:
				*dst = time.Duration(n * float64(time.Second))
				return nil
			case string:
				d, err := time.ParseDuration(n)
				if err != nil {
					return fmt.Errorf("invalid duration %q: %w", n, err)
				}
				*dst = d
				return nil
			default:
				return fmt.Errorf("expected seconds or a duration string, got %T", v)
			}
		}
	}
	hexList := func(dst *[]string) setFunc {
		return func(v any) error {
			arr, ok := v.([]any)
			if !ok {
				return fmt.Errorf("expected an array of hex strings, got %T", v)
			}
			out := make([]string, 0, len(arr))
			for _, el := range arr {
				s, ok := el.(string)
				if !ok {
					return fmt.Errorf("expected an array of hex strings, got element %T", el)
				}
				norm, err := adsb.NormalizeHex(s)
				if err != nil {
					return err
				}
				out = append(out, norm)
			}
			*dst = out
			return nil
		}
	}

	appliers := map[string]map[string]setFunc{
		"upstream": {
			"addr": str(&opts.UpstreamAddr),
			"type": str(&opts.UpstreamType),
		},
		"output": {
			"listen":      str(&opts.Listen),
			"http_listen": str(&opts.HTTPListen),
		},
		"decoder": {
			"url":           str(&opts.DecoderURL),
			"poll_interval": duration(&opts.PollInterval),
			"no_poll":       boolean(&opts.NoPoll),
		},
		"region": {
			"box":    str(&opts.Box),
			"circle": str(&opts.Circle),
		},
		"filters": {
			"above_ft":       number(&opts.Above),
			"below_ft":       number(&opts.Below),
			"allow_hex":      hexList(&opts.AllowHex),
			"deny_hex":       hexList(&opts.DenyHex),
			"military_only":  boolean(&opts.MilitaryOnly),
			"drop_on_ground": boolean(&opts.DropOnGround),
			"linger":         duration(&opts.Linger),
		},
	}
	for section, keys := range doc {
		for key, value := range keys {
			if err := appliers[section][key](value); err != nil {
				return fmt.Errorf("[%s] %s: %w", section, key, err)
			}
		}
	}
	// The decoder URL joins paths, so tolerate a trailing slash.
	opts.DecoderURL = strings.TrimRight(opts.DecoderURL, "/")
	return nil
}

// Validate reports configuration problems as operator-facing errors.
// Region syntax itself is checked by the region package once the specs are
// built.
func (o Options) Validate() error {
	switch o.UpstreamType {
	case UpstreamBeast, UpstreamSBS:
	default:
		return fmt.Errorf("--upstream-type must be %q or %q, got %q",
			UpstreamBeast, UpstreamSBS, o.UpstreamType)
	}
	if _, _, err := net.SplitHostPort(o.UpstreamAddr); err != nil {
		return fmt.Errorf("--upstream must be host:port, got %q: %w", o.UpstreamAddr, err)
	}
	if o.Listen == "" {
		return fmt.Errorf("--listen must not be empty")
	}
	if o.HTTPListen == "" {
		return fmt.Errorf("--http-listen must not be empty")
	}
	if o.Listen == o.HTTPListen {
		return fmt.Errorf("--listen and --http-listen must differ (both %q)", o.Listen)
	}
	if (o.Box == "") == (o.Circle == "") {
		return fmt.Errorf("exactly one region is required: set --box (GeoJSON file or minLat,minLon,maxLat,maxLon) or --circle lat,lon,radius_nm")
	}
	if o.Linger < 0 {
		return fmt.Errorf("--linger must not be negative, got %s", o.Linger)
	}
	if !o.NoPoll && o.PollInterval <= 0 {
		return fmt.Errorf("--poll-interval must be positive, got %s", o.PollInterval)
	}
	if o.NoPoll && o.UpstreamType == UpstreamBeast {
		return fmt.Errorf("--no-poll is only valid with --upstream-type sbs: Beast frames carry no position, so positions must come from the decoder's aircraft.json")
	}
	if o.Above > o.Below {
		return fmt.Errorf("--above (%g ft) must not exceed --below (%g ft)", o.Above, o.Below)
	}
	for _, h := range o.AllowHex {
		if _, err := adsb.NormalizeHex(h); err != nil {
			return fmt.Errorf("--allow-hex: %w", err)
		}
	}
	for _, h := range o.DenyHex {
		if _, err := adsb.NormalizeHex(h); err != nil {
			return fmt.Errorf("--deny-hex: %w", err)
		}
	}
	return nil
}
