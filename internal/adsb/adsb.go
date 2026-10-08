// Package adsb models the subset of a dump1090/readsb aircraft.json element
// that region filtering needs, while preserving the original object so the
// proxy can pass it through to its own filtered aircraft.json unchanged.
//
// Field semantics follow the decoder documentation: alt_baro is a number of
// feet or the string "ground" (ground is treated as 0 ft for altitude
// filters), hex is six lower-case hex digits and may carry a '~' prefix for
// non-ICAO addresses (TIS-B and friends), and dbFlags bit 0 marks military
// aircraft (readsb with --db-file / tar1090-db).
package adsb

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// hexRe matches ICAO addresses, including non-ICAO '~' addresses.
var hexRe = regexp.MustCompile(`^(?:~)?[0-9a-f]{6}$`)

// Aircraft is one aircraft.json element. Every field except Hex is optional;
// pointer fields distinguish "absent" from "zero" so partial observations
// (a bare SBS MSG,1 with only a callsign, say) can be merged without
// erasing known state.
type Aircraft struct {
	Hex       string          `json:"hex"`
	Flight    string          `json:"flight"`
	Lat       *float64        `json:"lat"`
	Lon       *float64        `json:"lon"`
	AltBaro   json.RawMessage `json:"alt_baro"` // feet as a JSON number, or the string "ground"
	AltGeom   *float64        `json:"alt_geom"`
	GS        *float64        `json:"gs"`
	Track     *float64        `json:"track"`
	Squawk    string          `json:"squawk"`
	Category  string          `json:"category"`
	Emergency string          `json:"emergency"`
	Type      string          `json:"type"`
	DBFlags   *int            `json:"dbFlags"`
	Messages  *int            `json:"messages"`
	RSSI      *float64        `json:"rssi"`
	Seen      *float64        `json:"seen"`
	SeenPos   *float64        `json:"seen_pos"`

	// Raw is the complete original JSON object (all keys, unknown ones
	// included). It is what the filtered aircraft.json hands to consumers.
	Raw map[string]any
}

// wire mirrors Aircraft for encoding/json; it exists so UnmarshalJSON can
// decode the typed view and the raw view from the same bytes without
// recursing.
type wire Aircraft

// UnmarshalJSON decodes one aircraft.json element into both the typed
// fields and the Raw passthrough map. Unknown keys are tolerated (and kept
// in Raw); a missing or malformed hex is an error because filtering by an
// address we could not parse would silently leak.
func (a *Aircraft) UnmarshalJSON(b []byte) error {
	var w wire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*a = Aircraft(w)

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	a.Raw = m

	hex, _ := m["hex"].(string)
	norm, err := NormalizeHex(hex)
	if err != nil {
		return fmt.Errorf("aircraft element: %w", err)
	}
	a.Hex = norm
	return nil
}

// Doc is the aircraft.json document shape served by both decoders.
type Doc struct {
	Now      float64    `json:"now"`
	Messages int        `json:"messages"`
	Aircraft []Aircraft `json:"aircraft"`
}

// ParseDoc decodes a full aircraft.json document. The returned aircraft are
// ordered as they appeared in the document.
func ParseDoc(data []byte) (*Doc, error) {
	var d Doc
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("parse aircraft.json: %w", err)
	}
	return &d, nil
}

// NormalizeHex validates and lower-cases a hex address. Both plain ICAO
// addresses (3c673a) and non-ICAO addresses (~4ca9f0) are accepted.
func NormalizeHex(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !hexRe.MatchString(s) {
		return "", fmt.Errorf("invalid hex address %q", s)
	}
	return s, nil
}

// AltFeet returns the barometric altitude in feet. The JSON string
// "ground" reports (0, true): readsb's query API documents ground as 0 ft
// "for simplicity/ versatility" and so do we. Absent or malformed alt_baro
// reports false, which makes altitude-constrained filters deny the aircraft
// (an unknown altitude cannot be proven to be inside the band).
func (a *Aircraft) AltFeet() (float64, bool) {
	if len(a.AltBaro) == 0 || string(a.AltBaro) == "null" {
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(a.AltBaro, &f); err == nil {
		return f, true
	}
	var s string
	if err := json.Unmarshal(a.AltBaro, &s); err == nil && s == "ground" {
		return 0, true
	}
	return 0, false
}

// OnGround reports whether the aircraft is on the ground. The decoder says
// so with alt_baro "ground"; there is no other in-band signal in
// aircraft.json.
func (a *Aircraft) OnGround() bool {
	if len(a.AltBaro) == 0 || string(a.AltBaro) == "null" {
		return false
	}
	var s string
	return json.Unmarshal(a.AltBaro, &s) == nil && s == "ground"
}

// Military reports dbFlags bit 0 (military = dbFlags & 1 per the readsb
// JSON docs). Aircraft from a decoder without an aircraft database have no
// dbFlags and are not military.
func (a *Aircraft) Military() bool {
	return a.DBFlags != nil && *a.DBFlags&1 != 0
}

// HasPosition reports whether a plausible lat/lon pair is present.
func (a *Aircraft) HasPosition() bool {
	if a.Lat == nil || a.Lon == nil {
		return false
	}
	return *a.Lat >= -90 && *a.Lat <= 90 && *a.Lon >= -180 && *a.Lon <= 180
}

// SetAltBaroFeet sets alt_baro to a plain number of feet.
func (a *Aircraft) SetAltBaroFeet(feet float64) {
	a.AltBaro = json.RawMessage(fmt.Sprintf("%g", feet))
}

// SetAltBaroGround sets alt_baro to the "ground" sentinel.
func (a *Aircraft) SetAltBaroGround() {
	a.AltBaro = json.RawMessage(`"ground"`)
}

// Merge folds a partial observation into a: fields the observation does not
// carry (nil pointers, empty strings, empty raw) leave a's value alone.
// Raw is replaced wholesale when the observation has one, because a raw
// object is always a complete upstream snapshot.
func (a *Aircraft) Merge(src *Aircraft) {
	if src.Hex != "" {
		a.Hex = src.Hex
	}
	if src.Flight != "" {
		a.Flight = src.Flight
	}
	if src.Lat != nil {
		a.Lat = src.Lat
	}
	if src.Lon != nil {
		a.Lon = src.Lon
	}
	if len(src.AltBaro) > 0 {
		a.AltBaro = src.AltBaro
	}
	if src.AltGeom != nil {
		a.AltGeom = src.AltGeom
	}
	if src.GS != nil {
		a.GS = src.GS
	}
	if src.Track != nil {
		a.Track = src.Track
	}
	if src.Squawk != "" {
		a.Squawk = src.Squawk
	}
	if src.Category != "" {
		a.Category = src.Category
	}
	if src.Emergency != "" {
		a.Emergency = src.Emergency
	}
	if src.Type != "" {
		a.Type = src.Type
	}
	if src.DBFlags != nil {
		a.DBFlags = src.DBFlags
	}
	if src.Messages != nil {
		a.Messages = src.Messages
	}
	if src.RSSI != nil {
		a.RSSI = src.RSSI
	}
	if src.Seen != nil {
		a.Seen = src.Seen
	}
	if src.SeenPos != nil {
		a.SeenPos = src.SeenPos
	}
	if src.Raw != nil {
		a.Raw = src.Raw
	}
}

// Clone returns a deep-enough copy for speculative filtering: pointer
// fields and the Raw map are shared, but mutating the copy's own fields
// cannot affect a. This is what lets the tracker evaluate an incoming
// update and discard it without having touched the stored state.
func (a *Aircraft) Clone() *Aircraft {
	c := *a
	return &c
}

// Ptr returns a pointer to v; the usual test helper.
func Ptr[T any](v T) *T { return &v }
