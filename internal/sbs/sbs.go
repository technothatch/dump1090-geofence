// Package sbs parses Basestation-format lines as dump1090 emits them on
// port 30003. The proxy never rewrites an accepted line: it forwards the
// original bytes, so parsing only has to extract the fields region
// filtering needs.
//
// dump1090's MSG layout (0-based comma-separated fields):
//
//	0 "MSG"  1 type  2 session  3 aircraft  4 hex  5 flight id
//	6 date generated  7 time generated  8 date logged  9 time logged
//	10 callsign  11 altitude (may carry an 'H' suffix: geometric)
//	12 ground speed  13 ground track  14 lat  15 lon  16 vertical rate
//	17 squawk  18 alert  19 emergency  20 ident  21 on-ground
//
// The on-ground flag is "-1" for ground, "0" for airborne and empty when
// unknown. Heartbeats on idle SBS connections are empty lines ("\r\n").
package sbs

import (
	"strconv"
	"strings"

	"github.com/technothatch/dump1090-geofence/internal/adsb"
)

// Field indices in a MSG line.
const (
	idxKind   = 0
	idxHex    = 4
	idxFlight = 10
	idxAlt    = 11
	idxGS     = 12
	idxTrack  = 13
	idxLat    = 14
	idxLon    = 15
	idxSquawk = 17
	idxGround = 21
	minFields = idxHex + 1
)

// Heartbeat is the empty line dump1090 writes on idle SBS connections.
const Heartbeat = "\r\n"

// IsHeartbeat reports whether line is the empty-line SBS heartbeat.
func IsHeartbeat(line []byte) bool {
	return len(strings.TrimSpace(string(line))) == 0
}

// Parse turns one SBS line into a partial aircraft observation. ok is
// false for heartbeats, non-MSG records (SEL, STA, ...) and anything
// without a parseable hex address — those must never reach the fence.
func Parse(line []byte) (*adsb.Aircraft, bool) {
	s := strings.TrimRight(string(line), "\r\n")
	if strings.TrimSpace(s) == "" {
		return nil, false
	}
	fields := strings.Split(s, ",")
	if len(fields) < minFields || fields[idxKind] != "MSG" {
		return nil, false
	}
	hex, err := adsb.NormalizeHex(fields[idxHex])
	if err != nil {
		return nil, false
	}
	obs := &adsb.Aircraft{Hex: hex}

	field := func(i int) string {
		if i >= len(fields) {
			return ""
		}
		return fields[i]
	}

	if fl := strings.TrimSpace(field(idxFlight)); fl != "" {
		obs.Flight = fl
	}
	if v, ok := parseFloat(strings.TrimSuffix(strings.ToUpper(field(idxAlt)), "H")); ok {
		obs.SetAltBaroFeet(v)
	}
	if v, ok := parseFloat(field(idxGS)); ok {
		obs.GS = &v
	}
	if v, ok := parseFloat(field(idxTrack)); ok {
		obs.Track = &v
	}
	if lat, okLat := parseFloat(field(idxLat)); okLat {
		if lon, okLon := parseFloat(field(idxLon)); okLon {
			obs.Lat, obs.Lon = &lat, &lon
		}
	}
	if sq := strings.TrimSpace(field(idxSquawk)); sq != "" {
		obs.Squawk = sq
	}
	// The ground flag is authoritative: it beats any altitude field,
	// mirroring how the decoder reports ground aircraft as alt_baro
	// "ground" in aircraft.json.
	if strings.TrimSpace(field(idxGround)) == "-1" {
		obs.SetAltBaroGround()
	}
	return obs, true
}

// parseFloat parses a possibly empty numeric field.
func parseFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}
