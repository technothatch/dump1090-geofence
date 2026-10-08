// Package fence holds the region policy and the aircraft state the proxy
// keeps while filtering.
//
// Decisions follow a fixed precedence so every deny has exactly one
// explainable reason (also used by --dry-run):
//
//	deny-hex beats everything; allow-hex bypasses everything else;
//	otherwise a candidate must have a position, be inside the region,
//	not be on the ground (if --drop-on-ground), have a provable
//	altitude inside the band (if a band is set), and be military (if
//	--military-only). Passing all of that is "inside-region".
package fence

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/technothatch/dump1090-geofence/internal/adsb"
	"github.com/technothatch/dump1090-geofence/internal/config"
	"github.com/technothatch/dump1090-geofence/internal/region"
)

// Reasons are the stable, machine-readable decision reasons.
const (
	ReasonDenyHex       = "deny-hex"
	ReasonAllowHex      = "allow-hex"
	ReasonNoPosition    = "no-position"
	ReasonOutsideRegion = "outside-region"
	ReasonOnGround      = "on-ground"
	ReasonUnknownAlt    = "unknown-altitude"
	ReasonBelowMinAlt   = "below-min-alt"
	ReasonAboveMaxAlt   = "above-max-alt"
	ReasonNotMilitary   = "not-military"
	ReasonInsideRegion  = "inside-region"
)

// Verdict is one decision about one candidate aircraft, carrying enough
// context for dry-run output.
type Verdict struct {
	Hex      string
	Allow    bool
	Reason   string
	OnGround bool
	Alt      *float64 // feet; nil when unknown
	Lat      *float64
	Lon      *float64
}

// Filter is the immutable region policy.
type Filter struct {
	reg          region.Region
	above, below float64
	allow        map[string]bool
	deny         map[string]bool
	militaryOnly bool
	dropOnGround bool
	linger       time.Duration
	hasBand      bool
}

// NewFilter builds the policy from the validated configuration and an
// already-constructed region. Hex lists are re-normalized so address
// matching cannot silently fail on case.
func NewFilter(reg region.Region, cfg config.Options) *Filter {
	f := &Filter{
		reg:          reg,
		above:        cfg.Above,
		below:        cfg.Below,
		allow:        make(map[string]bool, len(cfg.AllowHex)),
		deny:         make(map[string]bool, len(cfg.DenyHex)),
		militaryOnly: cfg.MilitaryOnly,
		dropOnGround: cfg.DropOnGround,
		linger:       cfg.Linger,
	}
	for _, h := range cfg.AllowHex {
		f.allow[strings.ToLower(strings.TrimSpace(h))] = true
	}
	for _, h := range cfg.DenyHex {
		f.deny[strings.ToLower(strings.TrimSpace(h))] = true
	}
	f.hasBand = cfg.Above > math.Inf(-1) || cfg.Below < math.Inf(+1)
	return f
}

// Linger returns how long an accepted aircraft stays in the snapshot after
// its last accepted observation; zero disables time-based expiry.
func (f *Filter) Linger() time.Duration { return f.linger }

// Region returns the configured region (for logs and health output).
func (f *Filter) Region() region.Region { return f.reg }

// Decide evaluates a candidate view of one aircraft. The candidate is the
// merged picture — held state plus the new observation — so partial
// messages (a callsign-only SBS line, say) are judged using what is
// already known about the aircraft.
func (f *Filter) Decide(c *adsb.Aircraft) Verdict {
	v := Verdict{Hex: c.Hex}
	if c.OnGround() {
		v.OnGround = true
	}
	if feet, ok := c.AltFeet(); ok {
		v.Alt = &feet
	}
	if c.HasPosition() {
		v.Lat, v.Lon = c.Lat, c.Lon
	}

	switch {
	case f.deny[v.Hex]:
		v.Reason = ReasonDenyHex
		return v // deny-hex wins over everything, including allow-hex
	case f.allow[v.Hex]:
		v.Allow = true
		v.Reason = ReasonAllowHex
		return v // allow-hex bypasses region, altitude and flag filters
	}

	if v.Lat == nil {
		v.Reason = ReasonNoPosition
		return v
	}
	if !f.reg.Contains(*v.Lat, *v.Lon) {
		v.Reason = ReasonOutsideRegion
		return v
	}
	if f.dropOnGround && v.OnGround {
		v.Reason = ReasonOnGround
		return v
	}
	if f.hasBand {
		if v.Alt == nil {
			v.Reason = ReasonUnknownAlt
			return v
		}
		if *v.Alt < f.above {
			v.Reason = ReasonBelowMinAlt
			return v
		}
		if *v.Alt > f.below {
			v.Reason = ReasonAboveMaxAlt
			return v
		}
	}
	if f.militaryOnly && !c.Military() {
		v.Reason = ReasonNotMilitary
		return v
	}
	v.Allow = true
	v.Reason = ReasonInsideRegion
	return v
}

// FormatDecision renders one verdict as a dry-run line:
//
//	hex=3c673a allow=true reason=inside-region alt=12000 pos=51.47000,-0.45000
func FormatDecision(v Verdict) string {
	alt := "-"
	switch {
	case v.OnGround:
		alt = "ground"
	case v.Alt != nil:
		alt = fmt.Sprintf("%g", *v.Alt)
	}
	pos := "-"
	if v.Lat != nil && v.Lon != nil {
		pos = fmt.Sprintf("%.5f,%.5f", *v.Lat, *v.Lon)
	}
	return fmt.Sprintf("hex=%s allow=%t reason=%s alt=%s pos=%s",
		v.Hex, v.Allow, v.Reason, alt, pos)
}
