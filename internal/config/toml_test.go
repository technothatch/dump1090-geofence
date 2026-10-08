package config

import (
	"strings"
	"testing"
)

func TestParseValidDocument(t *testing.T) {
	const doc = `# fence.toml
[upstream]
type = "sbs"          # inline comment
addr = "127.0.0.1:30003"

[output]
listen = ":40003"
http_listen = ":8082"

[decoder]
url = "http://decoder.local:8080/"
poll_interval = 2
no_poll = false

[region]
box = "polygon.geojson"

[filters]
above_ft = 1000
below_ft = 40000.5
allow_hex = [ "3C673A", "~abCDeF" ]
deny_hex = [
	"deadbe",   # always denied
	"000000",
]
military_only = true
drop_on_ground = true
linger = "45s"
`
	got, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	tests := []struct {
		section string
		key     string
		want    any
	}{
		{section: "upstream", key: "type", want: "sbs"},
		{section: "upstream", key: "addr", want: "127.0.0.1:30003"},
		{section: "output", key: "listen", want: ":40003"},
		{section: "output", key: "http_listen", want: ":8082"},
		{section: "decoder", key: "url", want: "http://decoder.local:8080/"},
		{section: "decoder", key: "poll_interval", want: int64(2)},
		{section: "decoder", key: "no_poll", want: false},
		{section: "region", key: "box", want: "polygon.geojson"},
		{section: "filters", key: "above_ft", want: int64(1000)},
		{section: "filters", key: "below_ft", want: 40000.5},
		{section: "filters", key: "military_only", want: true},
		{section: "filters", key: "drop_on_ground", want: true},
		{section: "filters", key: "linger", want: "45s"},
	}
	for _, tt := range tests {
		v, ok := got.section(tt.section)[tt.key]
		if !ok {
			t.Errorf("[%s] %s missing", tt.section, tt.key)
			continue
		}
		if v != tt.want {
			t.Errorf("[%s] %s = %#v, want %#v", tt.section, tt.key, v, tt.want)
		}
	}

	allow, ok := got.section("filters")["allow_hex"].([]any)
	if !ok || len(allow) != 2 {
		t.Fatalf("allow_hex = %#v, want 2-element array", got.section("filters")["allow_hex"])
	}
	if allow[0] != "3C673A" || allow[1] != "~abCDeF" {
		t.Errorf("allow_hex = %#v", allow)
	}
	deny, ok := got.section("filters")["deny_hex"].([]any)
	if !ok || len(deny) != 2 {
		t.Fatalf("deny_hex = %#v (multi-line array)", got.section("filters")["deny_hex"])
	}
	if deny[0] != "deadbe" || deny[1] != "000000" {
		t.Errorf("deny_hex = %#v", deny)
	}
}

func TestParseEmptyAndCommentsOnly(t *testing.T) {
	for _, in := range []string{"", "\n\n# just a comment\n", "   "} {
		doc, err := Parse([]byte(in))
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
		}
		if len(doc) != 0 {
			t.Errorf("Parse(%q) = %#v, want empty doc", in, doc)
		}
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantSub string
	}{
		{
			name:    "unknown value type",
			input:   "[filters]\nmilitary_only = yes\n",
			wantSub: "toml line 2",
		},
		{
			name:    "missing value",
			input:   "[region]\nbox =\n",
			wantSub: "missing value",
		},
		{
			name:    "duplicate key",
			input:   "[region]\nbox = \"a\"\nbox = \"b\"\n",
			wantSub: "duplicate key \"box\"",
		},
		{
			name:    "duplicate section",
			input:   "[region]\n[region]\n",
			wantSub: "duplicate section [region]",
		},
		{
			name:    "key before any section",
			input:   "box = \"polygon.geojson\"\n",
			wantSub: "before any [section]",
		},
		{
			name:    "unterminated string",
			input:   "[region]\nbox = \"polygon.geojson\n",
			wantSub: "unterminated string",
		},
		{
			name:    "unterminated array",
			input:   "[filters]\nallow_hex = [\"deadbe\",\n",
			wantSub: "unterminated value",
		},
		{
			name:    "nested array",
			input:   "[filters]\nallow_hex = [[\"a\"]]\n",
			wantSub: "nested arrays are not supported",
		},
		{
			name:    "nested section",
			input:   "[a.b]\nx = 1\n",
			wantSub: "nested tables are not supported",
		},
		{
			name:    "bare word value",
			input:   "[upstream]\ntype = beast\n",
			wantSub: "strings need quotes",
		},
		{
			name:    "unbalanced bracket",
			input:   "[filters]\nallow_hex = [\"a\"]]\n",
			wantSub: "unbalanced",
		},
		{
			name:    "missing key",
			input:   "[region]\n= \"x\"\n",
			wantSub: "missing key",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.input))
			if err == nil {
				t.Fatalf("Parse(%q) succeeded, want error containing %q", tt.input, tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("Parse error = %q, want substring %q", err, tt.wantSub)
			}
		})
	}
}

func TestParseScalarForms(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  any
	}{
		{name: "integer with underscores", input: "[s]\nk = 1_000\n", want: int64(1000)},
		{name: "negative integer", input: "[s]\nk = -42\n", want: int64(-42)},
		{name: "float", input: "[s]\nk = 12.25\n", want: 12.25},
		{name: "exponent", input: "[s]\nk = 1e3\n", want: 1000.0},
		{name: "bool false", input: "[s]\nk = false\n", want: false},
		{name: "literal string", input: "[s]\nk = 'raw # not a comment'\n", want: "raw # not a comment"},
		{name: "escaped string", input: `[s]` + "\n" + `k = "a\tb\"c"` + "\n", want: "a\tb\"c"},
		{name: "empty array", input: "[s]\nk = []\n", want: []any{}},
		{name: "trailing comma", input: "[s]\nk = [\"a\", \"b\",]\n", want: []any{"a", "b"}},
		{name: "array with comment line", input: "[s]\nk = [\n # note\n \"a\",\n]\n", want: []any{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := Parse([]byte(tt.input))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := doc.section("s")["k"]
			switch want := tt.want.(type) {
			case []any:
				arr, ok := got.([]any)
				if !ok || len(arr) != len(want) {
					t.Fatalf("k = %#v, want %#v", got, want)
				}
				for i := range want {
					if arr[i] != want[i] {
						t.Fatalf("k = %#v, want %#v", got, want)
					}
				}
			default:
				if got != want {
					t.Errorf("k = %#v (%T), want %#v (%T)", got, got, want, want)
				}
			}
		})
	}
}
