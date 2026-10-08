package adsb

import (
	"encoding/json"
	"testing"
)

func TestNormalizeHex(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "lower case ICAO", in: "3c673a", want: "3c673a"},
		{name: "upper case ICAO", in: "3C673A", want: "3c673a"},
		{name: "mixed with spaces", in: " AbCdEf ", want: "abcdef"},
		{name: "non-ICAO tilde address", in: "~4CA9F0", want: "~4ca9f0"},
		{name: "empty", in: "", wantErr: true},
		{name: "too short", in: "abc", wantErr: true},
		{name: "too long", in: "3c673a0", wantErr: true},
		{name: "non-hex digits", in: "3c673g", wantErr: true},
		{name: "tilde without address", in: "~", wantErr: true},
		{name: "double tilde", in: "~~3c67", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeHex(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeHex(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeHex(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("NormalizeHex(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestAltFeet(t *testing.T) {
	tests := []struct {
		name    string
		altBaro string // raw JSON, empty for absent
		want    float64
		wantOK  bool
	}{
		{name: "positive altitude", altBaro: `34000`, want: 34000, wantOK: true},
		{name: "negative altitude", altBaro: `-100`, want: -100, wantOK: true},
		{name: "zero altitude", altBaro: `0`, want: 0, wantOK: true},
		{name: "float altitude", altBaro: `1250.5`, want: 1250.5, wantOK: true},
		{name: "ground is zero feet", altBaro: `"ground"`, want: 0, wantOK: true},
		{name: "absent", altBaro: ``, want: 0, wantOK: false},
		{name: "null", altBaro: `null`, want: 0, wantOK: false},
		{name: "unexpected string", altBaro: `"fl240"`, want: 0, wantOK: false},
		{name: "object", altBaro: `{"ft":100}`, want: 0, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Aircraft{Hex: "abc123"}
			if tt.altBaro != "" {
				a.AltBaro = json.RawMessage(tt.altBaro)
			}
			got, ok := a.AltFeet()
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("AltFeet() = (%v, %v), want (%v, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestOnGround(t *testing.T) {
	tests := []struct {
		name    string
		altBaro string
		want    bool
	}{
		{name: "ground sentinel", altBaro: `"ground"`, want: true},
		{name: "numeric altitude", altBaro: `1200`, want: false},
		{name: "zero feet is not the ground sentinel", altBaro: `0`, want: false},
		{name: "absent", altBaro: ``, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Aircraft{Hex: "abc123"}
			if tt.altBaro != "" {
				a.AltBaro = json.RawMessage(tt.altBaro)
			}
			if got := a.OnGround(); got != tt.want {
				t.Errorf("OnGround() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMilitary(t *testing.T) {
	tests := []struct {
		name    string
		dbFlags *int
		want    bool
	}{
		{name: "military bit set", dbFlags: Ptr(1), want: true},
		{name: "military combined with interesting", dbFlags: Ptr(3), want: true},
		{name: "only interesting bit", dbFlags: Ptr(2), want: false},
		{name: "zero flags", dbFlags: Ptr(0), want: false},
		{name: "pia and ladd only", dbFlags: Ptr(12), want: false},
		{name: "no database flags", dbFlags: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Aircraft{Hex: "abc123", DBFlags: tt.dbFlags}
			if got := a.Military(); got != tt.want {
				t.Errorf("Military() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasPosition(t *testing.T) {
	tests := []struct {
		name string
		lat  *float64
		lon  *float64
		want bool
	}{
		{name: "both present", lat: Ptr(51.5), lon: Ptr(-0.1), want: true},
		{name: "null island", lat: Ptr(0.0), lon: Ptr(0.0), want: true},
		{name: "lat only", lat: Ptr(51.5), want: false},
		{name: "lon only", lon: Ptr(-0.1), want: false},
		{name: "neither", want: false},
		{name: "latitude out of range", lat: Ptr(91.0), lon: Ptr(0.0), want: false},
		{name: "longitude out of range", lat: Ptr(0.0), lon: Ptr(181.0), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Aircraft{Hex: "abc123", Lat: tt.lat, Lon: tt.lon}
			if got := a.HasPosition(); got != tt.want {
				t.Errorf("HasPosition() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUnmarshalRealisticElement(t *testing.T) {
	// A trimmed but realistic aircraft.json element: every field the filter
	// reads must survive, unknown keys must be preserved in Raw, and a
	// non-ICAO '~' hex must normalize.
	const payload = `{
		"hex": "~4CA9F0",
		"type": "adsb_icao",
		"flight": "BAW117  ",
		"alt_baro": "ground",
		"gs": 0,
		"track": 84.1,
		"squawk": "7700",
		"category": "A1",
		"lat": 51.47,
		"lon": -0.45,
		"dbFlags": 1,
		"messages": 42,
		"seen": 0.4,
		"rssi": -3.2,
		"some_future_field": {"nested": true}
	}`
	var a Aircraft
	if err := json.Unmarshal([]byte(payload), &a); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if a.Hex != "~4ca9f0" {
		t.Errorf("Hex = %q, want ~4ca9f0", a.Hex)
	}
	if a.Flight != "BAW117  " {
		t.Errorf("Flight = %q, want preserved verbatim", a.Flight)
	}
	if !a.OnGround() {
		t.Error("OnGround() = false, want true")
	}
	if alt, ok := a.AltFeet(); !ok || alt != 0 {
		t.Errorf("AltFeet() = (%v, %v), want (0, true)", alt, ok)
	}
	if !a.Military() {
		t.Error("Military() = false, want true")
	}
	if !a.HasPosition() {
		t.Error("HasPosition() = false, want true")
	}
	if a.Messages == nil || *a.Messages != 42 {
		t.Errorf("Messages = %v, want 42", a.Messages)
	}
	if _, ok := a.Raw["some_future_field"]; !ok {
		t.Error("Raw dropped the unknown field")
	}
	if a.Raw["squawk"] != "7700" {
		t.Errorf("Raw[squawk] = %v, want 7700", a.Raw["squawk"])
	}
}

func TestUnmarshalRejectsInvalidHex(t *testing.T) {
	for _, payload := range []string{
		`{"hex": "xyz"}`,
		`{"hex": ""}`,
		`{}`,
	} {
		var a Aircraft
		if err := json.Unmarshal([]byte(payload), &a); err == nil {
			t.Errorf("unmarshal(%s) succeeded, want hex error", payload)
		}
	}
}

func TestParseDoc(t *testing.T) {
	const payload = `{
		"now": 1760000000,
		"messages": 123456,
		"aircraft": [
			{"hex": "3c673a", "lat": 51.5, "lon": -0.1, "alt_baro": 34000},
			{"hex": "abc123", "flight": "TEST123"}
		]
	}`
	doc, err := ParseDoc([]byte(payload))
	if err != nil {
		t.Fatalf("ParseDoc: %v", err)
	}
	if doc.Messages != 123456 {
		t.Errorf("Messages = %d, want 123456", doc.Messages)
	}
	if len(doc.Aircraft) != 2 {
		t.Fatalf("len(Aircraft) = %d, want 2", len(doc.Aircraft))
	}
	if doc.Aircraft[0].Hex != "3c673a" {
		t.Errorf("first hex = %q", doc.Aircraft[0].Hex)
	}
	if alt, ok := doc.Aircraft[0].AltFeet(); !ok || alt != 34000 {
		t.Errorf("first AltFeet = (%v, %v)", alt, ok)
	}
	if doc.Aircraft[1].Flight != "TEST123" {
		t.Errorf("second flight = %q", doc.Aircraft[1].Flight)
	}
}

func TestParseDocRejectsGarbage(t *testing.T) {
	if _, err := ParseDoc([]byte(`not json`)); err == nil {
		t.Error("ParseDoc(garbage) succeeded, want error")
	}
}

func TestMerge(t *testing.T) {
	base := &Aircraft{
		Hex:      "3c673a",
		Flight:   "BAW117",
		Lat:      Ptr(51.5),
		Lon:      Ptr(-0.1),
		AltBaro:  json.RawMessage(`34000`),
		Squawk:   "1200",
		Messages: Ptr(10),
	}
	t.Run("partial update keeps unknown fields", func(t *testing.T) {
		got := base.Clone()
		got.Merge(&Aircraft{Hex: "3c673a", Squawk: "7700"})
		if got.Squawk != "7700" {
			t.Errorf("Squawk = %q, want 7700", got.Squawk)
		}
		if got.Flight != "BAW117" {
			t.Errorf("Flight = %q, want preserved", got.Flight)
		}
		if got.Lat == nil || *got.Lat != 51.5 {
			t.Errorf("Lat = %v, want preserved 51.5", got.Lat)
		}
		if got.Messages == nil || *got.Messages != 10 {
			t.Errorf("Messages = %v, want preserved 10", got.Messages)
		}
		if base.Squawk != "1200" {
			t.Errorf("merge mutated the source: Squawk = %q", base.Squawk)
		}
	})
	t.Run("observation without position keeps stored position", func(t *testing.T) {
		got := base.Clone()
		got.Merge(&Aircraft{Hex: "3c673a", Flight: "BAW117"})
		if got.Lat == nil || got.Lon == nil {
			t.Errorf("position cleared by a position-less observation: %v/%v", got.Lat, got.Lon)
		}
	})
	t.Run("raw replaces wholesale", func(t *testing.T) {
		got := base.Clone()
		got.Merge(&Aircraft{Hex: "3c673a", Raw: map[string]any{"hex": "3c673a"}})
		if len(got.Raw) != 1 {
			t.Errorf("Raw = %v, want replaced map", got.Raw)
		}
	})
}

func TestSetAltBaro(t *testing.T) {
	a := &Aircraft{Hex: "abc123"}
	a.SetAltBaroFeet(1200)
	if alt, ok := a.AltFeet(); !ok || alt != 1200 {
		t.Errorf("AltFeet() = (%v, %v), want (1200, true)", alt, ok)
	}
	a.SetAltBaroGround()
	if !a.OnGround() {
		t.Error("OnGround() = false after SetAltBaroGround")
	}
}
