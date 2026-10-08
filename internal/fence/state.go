package fence

import (
	"sort"
	"sync"
	"time"

	"github.com/technothatch/dump1090-geofence/internal/adsb"
)

// entry is the held state for one allowed aircraft.
//
// State is committed only when an observation is allowed: a denied
// observation leaves the entry exactly as it was (frozen), so data from an
// aircraft that left the region can never be refreshed or re-emitted from
// stale inside-the-fence knowledge. The entry then ages out via linger.
type entry struct {
	ac            *adsb.Aircraft
	lastAccepted  time.Time
	lastPosAccept time.Time
	messages      int
}

// Tracker is the concurrency-safe aircraft state behind the filter: it
// decides observations, commits allowed ones, forwards what is currently
// vouched for, and emits the filtered aircraft.json snapshot.
type Tracker struct {
	mu       sync.Mutex
	f        *Filter
	entries  map[string]*entry
	now      func() time.Time
	messages int // decoder message count for the snapshot
}

// NewTracker creates an empty tracker over an already-built policy. The
// clock starts at time.Now; the package tests replace it for
// deterministic expiry.
func NewTracker(f *Filter) *Tracker {
	return &Tracker{
		f:       f,
		entries: make(map[string]*entry),
		now:     time.Now,
	}
}

// Observe folds one observation (an SBS line, or one aircraft from a
// polled aircraft.json) into the state:
//
//   - judged on the merged view (held state + observation);
//   - allowed observations are committed and refresh the entry's lease;
//   - denied observations are never merged in (frozen state);
//   - deny-hex observations delete any held entry immediately.
//
// The verdict it returns is what --dry-run prints per input record.
func (t *Tracker) Observe(obs *adsb.Aircraft) Verdict {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	hex := obs.Hex
	t.messages++

	e := t.entries[hex]
	var merged *adsb.Aircraft
	if e != nil {
		merged = e.ac.Clone()
		merged.Merge(obs)
	} else {
		merged = obs
	}

	v := t.f.Decide(merged)
	switch {
	case v.Allow:
		if e == nil {
			e = &entry{ac: obs.Clone()}
			t.entries[hex] = e
		} else {
			e.ac = merged
		}
		e.lastAccepted = now
		if merged.HasPosition() {
			e.lastPosAccept = now
		}
		e.messages++
	case v.Reason == ReasonDenyHex:
		delete(t.entries, hex)
	default:
		// Frozen: keep the previously accepted state untouched.
		if e != nil {
			e.messages++
		}
	}
	return v
}

// CanForward reports whether a message with this hex address may be
// forwarded right now. Entries exist only for allowed aircraft, and expire
// once they have outlived linger.
func (t *Tracker) CanForward(hex string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	e, ok := t.entries[hex]
	if !ok {
		return false
	}
	return !t.expired(e, t.now())
}

// CountMessage counts a message the state machine saw without an
// observation to merge (a Beast frame carries no position), refreshing
// nothing: only observations vouch for an aircraft.
func (t *Tracker) CountMessage(hex string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.messages++
	if e, ok := t.entries[hex]; ok {
		e.messages++
	}
}

// SetMessages replaces the decoder message count, used when the upstream
// aircraft.json provides the authoritative total.
func (t *Tracker) SetMessages(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.messages = n
}

// Prune drops entries that have outlived linger. Callers tick it
// periodically so stale aircraft leave the snapshot even when nobody
// requests it.
func (t *Tracker) Prune() {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	for h, e := range t.entries {
		if t.expired(e, now) {
			delete(t.entries, h)
		}
	}
}

// expired reports whether an entry has outlived linger. Linger 0 disables
// time-based expiry entirely (entries then live until deny-hex or
// process exit), matching the documented "linger > 0 ? age <= linger :
// keep" rule.
func (t *Tracker) expired(e *entry, now time.Time) bool {
	if t.f.Linger() <= 0 {
		return false
	}
	return now.Sub(e.lastAccepted) > t.f.Linger()
}

// SnapshotDoc is the filtered aircraft.json document the proxy serves.
type SnapshotDoc struct {
	Now      float64          `json:"now"`
	Messages int              `json:"messages"`
	Aircraft []map[string]any `json:"aircraft"`
}

// Snapshot prunes expired entries and renders the current state as an
// aircraft.json document: every aircraft is the original upstream object
// (all keys preserved) with seen/seen_pos/messages recomputed for the
// filtered view. Aircraft are sorted by hex for deterministic output.
func (t *Tracker) Snapshot() *SnapshotDoc {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	for h, e := range t.entries {
		if t.expired(e, now) {
			delete(t.entries, h)
		}
	}

	doc := &SnapshotDoc{
		Now:      float64(now.Unix()),
		Messages: t.messages,
		Aircraft: make([]map[string]any, 0, len(t.entries)),
	}
	for _, e := range t.entries {
		doc.Aircraft = append(doc.Aircraft, e.view(now))
	}
	sort.Slice(doc.Aircraft, func(i, j int) bool {
		hi, _ := doc.Aircraft[i]["hex"].(string)
		hj, _ := doc.Aircraft[j]["hex"].(string)
		return hi < hj
	})
	return doc
}

// Len reports how many aircraft are currently held (tests, health).
func (t *Tracker) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}

// view renders one entry as an aircraft.json object.
func (e *entry) view(now time.Time) map[string]any {
	m := make(map[string]any, len(e.ac.Raw)+4)
	for k, v := range e.ac.Raw {
		m[k] = v
	}
	m["hex"] = e.ac.Hex

	// SBS-built entries carry no Raw object; fill the typed fields.
	set := func(key string, val any) {
		if _, ok := m[key]; !ok {
			m[key] = val
		}
	}
	if e.ac.Flight != "" {
		set("flight", e.ac.Flight)
	}
	if e.ac.Lat != nil {
		set("lat", *e.ac.Lat)
	}
	if e.ac.Lon != nil {
		set("lon", *e.ac.Lon)
	}
	if e.ac.GS != nil {
		set("gs", *e.ac.GS)
	}
	if e.ac.Track != nil {
		set("track", *e.ac.Track)
	}
	if e.ac.Squawk != "" {
		set("squawk", e.ac.Squawk)
	}
	if _, ok := m["alt_baro"]; !ok {
		switch {
		case e.ac.OnGround():
			m["alt_baro"] = "ground"
		default:
			if feet, known := e.ac.AltFeet(); known {
				m["alt_baro"] = feet
			}
		}
	}

	m["seen"] = now.Sub(e.lastAccepted).Seconds()
	if !e.lastPosAccept.IsZero() {
		m["seen_pos"] = now.Sub(e.lastPosAccept).Seconds()
	}
	m["messages"] = e.messages
	return m
}
