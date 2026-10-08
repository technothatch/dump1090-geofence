package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fence.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestDefaults(t *testing.T) {
	o := Defaults()
	if o.UpstreamType != UpstreamBeast || o.UpstreamAddr != DefaultUpstream {
		t.Errorf("upstream = %s/%s", o.UpstreamType, o.UpstreamAddr)
	}
	if o.Listen != ":30006" || o.HTTPListen != ":8081" {
		t.Errorf("listen = %s / %s", o.Listen, o.HTTPListen)
	}
	if o.Linger != 30*time.Second {
		t.Errorf("Linger = %s, want 30s", o.Linger)
	}
	if o.PollInterval != time.Second {
		t.Errorf("PollInterval = %s, want 1s", o.PollInterval)
	}
	if !math.IsInf(o.Above, -1) || !math.IsInf(o.Below, +1) {
		t.Errorf("altitude band = [%v, %v], want [-Inf, +Inf]", o.Above, o.Below)
	}
	if o.Box != "" || o.Circle != "" {
		t.Error("defaults must not configure a region")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Error("Load(missing) succeeded, want error")
	}
}

func TestLoadNoFileKeepsDefaults(t *testing.T) {
	o, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if o.UpstreamAddr != DefaultUpstream {
		t.Errorf("UpstreamAddr = %q", o.UpstreamAddr)
	}
}

func TestLoadFullFile(t *testing.T) {
	path := writeConfig(t, `
[upstream]
type = "sbs"
addr = "10.0.0.2:30003"

[output]
listen = ":30010"
http_listen = ":8090"

[decoder]
url = "http://10.0.0.2:8080"
poll_interval = "250ms"
no_poll = true

[region]
circle = "51.5,-0.1,40"

[filters]
above_ft = 0
below_ft = 38000
allow_hex = ["3c673a"]
deny_hex = ["deadbe"]
military_only = true
drop_on_ground = true
linger = 15
`)
	o, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if o.UpstreamType != UpstreamSBS || o.UpstreamAddr != "10.0.0.2:30003" {
		t.Errorf("upstream = %s/%s", o.UpstreamType, o.UpstreamAddr)
	}
	if o.Listen != ":30010" || o.HTTPListen != ":8090" {
		t.Errorf("listen = %s / %s", o.Listen, o.HTTPListen)
	}
	if o.DecoderURL != "http://10.0.0.2:8080" {
		t.Errorf("DecoderURL = %q", o.DecoderURL)
	}
	if o.PollInterval != 250*time.Millisecond {
		t.Errorf("PollInterval = %s", o.PollInterval)
	}
	if !o.NoPoll {
		t.Error("NoPoll = false, want true")
	}
	if o.Circle != "51.5,-0.1,40" || o.Box != "" {
		t.Errorf("region = box %q circle %q", o.Box, o.Circle)
	}
	if o.Above != 0 || o.Below != 38000 {
		t.Errorf("band = [%v, %v]", o.Above, o.Below)
	}
	if len(o.AllowHex) != 1 || o.AllowHex[0] != "3c673a" {
		t.Errorf("AllowHex = %v", o.AllowHex)
	}
	if len(o.DenyHex) != 1 || o.DenyHex[0] != "deadbe" {
		t.Errorf("DenyHex = %v", o.DenyHex)
	}
	if !o.MilitaryOnly || !o.DropOnGround {
		t.Error("military_only/drop_on_ground not applied")
	}
	if o.Linger != 15*time.Second {
		t.Errorf("Linger = %s, want 15s", o.Linger)
	}
	if err := o.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestLoadNormalizesHexAndStripsURLSlash(t *testing.T) {
	path := writeConfig(t, `
[decoder]
url = "http://decoder:8080/"
[region]
box = "polygon.geojson"
[filters]
allow_hex = ["AABBCC"]
deny_hex = ["~DDEEFF"]
`)
	o, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if o.AllowHex[0] != "aabbcc" || o.DenyHex[0] != "~ddeeff" {
		t.Errorf("hex lists not normalized: %v / %v", o.AllowHex, o.DenyHex)
	}
	if o.DecoderURL != "http://decoder:8080" {
		t.Errorf("DecoderURL = %q", o.DecoderURL)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantSub string
	}{
		{name: "unknown section", body: "[wat]\nx = 1\n", wantSub: "unknown section [wat]"},
		{name: "unknown key", body: "[filters]\nallow_hx = [\"abc123\"]\n", wantSub: `unknown key "allow_hx"`},
		{name: "bad hex list element", body: "[filters]\nallow_hex = [\"nothex\"]\n", wantSub: "invalid hex address"},
		{name: "hex list not an array", body: "[filters]\nallow_hex = \"abc123\"\n", wantSub: "expected an array"},
		{name: "wrong type", body: "[filters]\nmilitary_only = 1\n", wantSub: "expected true/false"},
		{name: "bad duration", body: "[filters]\nlinger = \"soon\"\n", wantSub: "invalid duration"},
		{name: "array where string expected", body: "[region]\nbox = [\"a\"]\n", wantSub: "expected a string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.body))
			if err == nil {
				t.Fatalf("Load succeeded, want error containing %q", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want substring %q", err, tt.wantSub)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	valid := func() Options {
		o := Defaults()
		o.Box = "polygon.geojson"
		return o
	}

	tests := []struct {
		name    string
		mutate  func(*Options)
		wantSub string
	}{
		{name: "valid defaults with region", mutate: func(*Options) {}},
		{
			name:    "unknown upstream type",
			mutate:  func(o *Options) { o.UpstreamType = "avr" },
			wantSub: "--upstream-type",
		},
		{
			name:    "upstream without port",
			mutate:  func(o *Options) { o.UpstreamAddr = "localhost" },
			wantSub: "host:port",
		},
		{
			name:    "no region",
			mutate:  func(o *Options) { o.Box = "" },
			wantSub: "exactly one region is required",
		},
		{
			name:    "both regions",
			mutate:  func(o *Options) { o.Circle = "51.5,-0.1,40" },
			wantSub: "exactly one region is required",
		},
		{
			name:    "negative linger",
			mutate:  func(o *Options) { o.Linger = -time.Second },
			wantSub: "--linger",
		},
		{
			name:    "non-positive poll interval",
			mutate:  func(o *Options) { o.PollInterval = 0 },
			wantSub: "--poll-interval",
		},
		{
			name: "no-poll with beast upstream",
			mutate: func(o *Options) {
				o.NoPoll = true
				o.UpstreamType = UpstreamBeast
			},
			wantSub: "--no-poll is only valid",
		},
		{
			name: "inverted altitude band",
			mutate: func(o *Options) {
				o.Above = 10000
				o.Below = 1000
			},
			wantSub: "--above",
		},
		{
			name:    "empty listen address",
			mutate:  func(o *Options) { o.Listen = "" },
			wantSub: "--listen",
		},
		{
			name:    "listen equals http-listen",
			mutate:  func(o *Options) { o.HTTPListen = o.Listen },
			wantSub: "must differ",
		},
		{
			name:    "invalid allow hex",
			mutate:  func(o *Options) { o.AllowHex = []string{"nope"} },
			wantSub: "--allow-hex",
		},
		{
			name:    "invalid deny hex",
			mutate:  func(o *Options) { o.DenyHex = []string{"zzzzzz"} },
			wantSub: "--deny-hex",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := valid()
			tt.mutate(&o)
			err := o.Validate()
			if tt.wantSub == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate succeeded, want error containing %q", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want substring %q", err, tt.wantSub)
			}
		})
	}
}
