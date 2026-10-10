package sbs

import (
	"strings"
	"testing"

	"github.com/technothatch/dump1090-geofence/internal/adsb"
)

// msgLine assembles a 22-field MSG line from the given values; empty
// entries become empty fields.
func msgLine(fields ...string) string {
	return "MSG," + strings.Join(fields, ",")
}

func TestParseFullPositionMessage(t *testing.T) {
	line := msgLine(
		"3", "1", "1", "ABCDEF", "1",
		"2026/10/06", "12:00:00.000", "2026/10/06", "12:00:00.000",
		"BAW123 ", "12000", "400", "270.5",
		"51.50000", "-0.10000", "0", "7000",
		"-1", "0", "0", "0",
	)
	obs, ok := Parse([]byte(line + "\r\n"))
	if !ok {
		t.Fatal("Parse not ok")
	}
	if obs.Hex != "abcdef" {
		t.Errorf("Hex = %q", obs.Hex)
	}
	if obs.Flight != "BAW123" {
		t.Errorf("Flight = %q, want trimmed BAW123", obs.Flight)
	}
	if alt, ok := obs.AltFeet(); !ok || alt != 12000 {
		t.Errorf("alt = %v, %v", alt, ok)
	}
	if obs.GS == nil || *obs.GS != 400 {
		t.Errorf("GS = %v", obs.GS)
	}
	if obs.Track == nil || *obs.Track != 270.5 {
		t.Errorf("Track = %v", obs.Track)
	}
	if !obs.HasPosition() || *obs.Lat != 51.5 || *obs.Lon != -0.1 {
		t.Errorf("position = %v, %v", obs.Lat, obs.Lon)
	}
	if obs.Squawk != "7000" {
		t.Errorf("Squawk = %q", obs.Squawk)
	}
	if obs.OnGround() {
		t.Error("airborne message reported on ground")
	}
}

func TestParseMinimalCallsignMessage(t *testing.T) {
	// MSG,1 (identification): only the callsign is filled in.
	line := msgLine(
		"1", "1", "1", "3C673A", "1",
		"2026/10/06", "12:00:00.000", "2026/10/06", "12:00:00.000",
		"EZY833",
	)
	obs, ok := Parse([]byte(line))
	if !ok {
		t.Fatal("Parse not ok")
	}
	if obs.Flight != "EZY833" {
		t.Errorf("Flight = %q", obs.Flight)
	}
	if _, known := obs.AltFeet(); known {
		t.Error("altitude known but message carried none")
	}
	if obs.HasPosition() {
		t.Error("position known but message carried none")
	}
	if obs.OnGround() {
		t.Error("ground state known but message carried none")
	}
}

func TestParseGeometricAltitudeSuffix(t *testing.T) {
	// args map to fields 1.., so altitude (field 11) is arg 10.
	line := msgLine("5", "1", "1", "ABCDEF", "1", "", "", "", "", "", "38000H")
	obs, ok := Parse([]byte(line))
	if !ok {
		t.Fatal("Parse not ok")
	}
	if alt, known := obs.AltFeet(); !known || alt != 38000 {
		t.Errorf("alt = %v, %v (H suffix)", alt, known)
	}
}

func TestParseNegativeAltitude(t *testing.T) {
	line := msgLine("5", "1", "1", "ABCDEF", "1", "", "", "", "", "", "-500")
	obs, ok := Parse([]byte(line))
	if !ok {
		t.Fatal("Parse not ok")
	}
	if alt, known := obs.AltFeet(); !known || alt != -500 {
		t.Errorf("alt = %v, %v", alt, known)
	}
}

func TestParseGroundFlagWinsOverAltitude(t *testing.T) {
	// Field 22 (-1) means on ground; dump1090 may still write the last
	// barometric altitude, but the ground flag is what the decoder's own
	// aircraft.json would report as alt_baro "ground".
	line := msgLine(
		"5", "1", "1", "ABCDEF", "1", "", "", "", "", "", "50",
		"", "", "", "", "", "", "", "", "", "-1",
	)
	obs, ok := Parse([]byte(line))
	if !ok {
		t.Fatal("Parse not ok")
	}
	if !obs.OnGround() {
		t.Error("ground flag ignored")
	}
	if alt, known := obs.AltFeet(); !known || alt != 0 {
		t.Errorf("alt = %v, %v, want 0/true (ground)", alt, known)
	}
}

func TestParseAirborneFlagKeepsAltitude(t *testing.T) {
	line := msgLine(
		"5", "1", "1", "ABCDEF", "1", "", "", "", "", "", "35000",
		"", "", "", "", "", "", "", "", "", "0",
	)
	obs, ok := Parse([]byte(line))
	if !ok {
		t.Fatal("Parse not ok")
	}
	if obs.OnGround() {
		t.Error("airborne flag reported ground")
	}
	if alt, known := obs.AltFeet(); !known || alt != 35000 {
		t.Errorf("alt = %v, %v", alt, known)
	}
}

func TestParseIncompletePositionDropped(t *testing.T) {
	// Lat without lon: neither may be set, otherwise the fence would see
	// a half-position as a fix.
	line := msgLine("5", "1", "1", "ABCDEF", "1", "", "", "", "", "", "", "", "", "51.5", "")
	obs, ok := Parse([]byte(line))
	if !ok {
		t.Fatal("Parse not ok")
	}
	if obs.Lat != nil || obs.Lon != nil {
		t.Errorf("position = %v, %v; want nil, nil", obs.Lat, obs.Lon)
	}
}

func TestParseUnparseableNumericFieldsIgnored(t *testing.T) {
	line := msgLine("5", "1", "1", "ABCDEF", "1", "", "", "", "", "", "?", "fast", "north", "abc", "def")
	obs, ok := Parse([]byte(line))
	if !ok {
		t.Fatal("Parse not ok")
	}
	if _, known := obs.AltFeet(); known {
		t.Error("altitude parsed from junk")
	}
	if obs.GS != nil || obs.Track != nil || obs.Lat != nil || obs.Lon != nil {
		t.Errorf("junk parsed: gs=%v track=%v lat=%v lon=%v", obs.GS, obs.Track, obs.Lat, obs.Lon)
	}
}

func TestParseRejections(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "empty", line: ""},
		{name: "heartbeat crlf", line: "\r\n"},
		{name: "heartbeat spaces", line: "  \r\n"},
		{name: "other record type", line: "SEL,1,1,1,ABCDEF,1"},
		{name: "too few fields", line: "MSG,3,1,1"},
		{name: "bad hex letters", line: msgLine("3", "1", "1", "XYZ123", "1")},
		{name: "short hex", line: msgLine("3", "1", "1", "12345", "1")},
		{name: "empty hex", line: msgLine("3", "1", "1", "", "1")},
		{name: "garbage", line: "hello, world"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if obs, ok := Parse([]byte(tt.line)); ok {
				t.Errorf("Parse(%q) = %v, want not ok", tt.line, obs)
			}
		})
	}
}

func TestIsHeartbeat(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{line: "\r\n", want: true},
		{line: "", want: true},
		{line: " \t\r\n", want: true},
		{line: "MSG,1\r\n", want: false},
	}
	for _, tt := range tests {
		if got := IsHeartbeat([]byte(tt.line)); got != tt.want {
			t.Errorf("IsHeartbeat(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

func TestParseNormalizesHexCase(t *testing.T) {
	line := msgLine("3", "1", "1", "AbCdEf", "1")
	obs, ok := Parse([]byte(line))
	if !ok {
		t.Fatal("Parse not ok")
	}
	if obs.Hex != "abcdef" {
		t.Errorf("Hex = %q, want abcdef", obs.Hex)
	}
	if _, ok := any(obs).(*adsb.Aircraft); !ok {
		t.Fatalf("obs has type %T, want *adsb.Aircraft", obs)
	}
}
