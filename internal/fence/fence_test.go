package fence

import (
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/technothatch/dump1090-geofence/internal/adsb"
	"github.com/technothatch/dump1090-geofence/internal/config"
	"github.com/technothatch/dump1090-geofence/internal/region"
)

var (
	insideLat  = 51.47
	insideLon  = -0.45
	outsideLat = 60.0
	outsideLon = 10.0
)

// testFilter builds a filter over a box around south-east England with the
// caller's option changes applied.
func testFilter(t *testing.T, mutate func(*config.Options)) *Filter {
	t.Helper()
	cfg := config.Defaults()
	cfg.Box = "50,-5,53,2"
	if mutate != nil {
		mutate(&cfg)
	}
	reg, err := region.Build(cfg.Box, cfg.Circle)
	if err != nil {
		t.Fatalf("region: %v", err)
	}
	return NewFilter(reg, cfg)
}

// obs builds a full observation: position, altitude 10000 ft, airborne.
func obs(hex, flight string, lat, lon float64) *adsb.Aircraft {
	a := &adsb.Aircraft{Hex: hex, Flight: flight}
	a.Lat = &lat
	a.Lon = &lon
	a.SetAltBaroFeet(10000)
	return a
}

func TestDecideTable(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*config.Options)
		cand       *adsb.Aircraft
		wantAllow  bool
		wantReason string
	}{
		{
			name:       "inside passes by default",
			cand:       obs("3c673a", "BAW123", insideLat, insideLon),
			wantAllow:  true,
			wantReason: ReasonInsideRegion,
		},
		{
			name:       "outside region",
			cand:       obs("3c673a", "", outsideLat, outsideLon),
			wantAllow:  false,
			wantReason: ReasonOutsideRegion,
		},
		{
			name:       "no position",
			cand:       &adsb.Aircraft{Hex: "3c673a"},
			wantAllow:  false,
			wantReason: ReasonNoPosition,
		},
		{
			name: "deny-hex wins",
			mutate: func(c *config.Options) {
				c.DenyHex = []string{"3c673a"}
				c.AllowHex = []string{"3c673a"}
			},
			cand:       obs("3c673a", "", insideLat, insideLon),
			wantAllow:  false,
			wantReason: ReasonDenyHex,
		},
		{
			name: "allow-hex bypasses region",
			mutate: func(c *config.Options) {
				c.AllowHex = []string{"3C673A"} // upper case on purpose
			},
			cand:       obs("3c673a", "", outsideLat, outsideLon),
			wantAllow:  true,
			wantReason: ReasonAllowHex,
		},
		{
			name: "allow-hex bypasses missing position",
			mutate: func(c *config.Options) {
				c.AllowHex = []string{"3c673a"}
			},
			cand:       &adsb.Aircraft{Hex: "3c673a"},
			wantAllow:  true,
			wantReason: ReasonAllowHex,
		},
		{
			name: "altitude inside band",
			mutate: func(c *config.Options) {
				c.Above = 3000
				c.Below = 40000
			},
			cand:       obs("3c673a", "", insideLat, insideLon), // 10000 ft
			wantAllow:  true,
			wantReason: ReasonInsideRegion,
		},
		{
			name: "altitude under minimum",
			mutate: func(c *config.Options) {
				c.Above = 20000
			},
			cand:       obs("3c673a", "", insideLat, insideLon), // 10000 < 20000
			wantAllow:  false,
			wantReason: ReasonBelowMinAlt,
		},
		{
			name: "altitude over maximum",
			mutate: func(c *config.Options) {
				c.Above = -1
				c.Below = 5000
			},
			cand:       obs("3c673a", "", insideLat, insideLon), // 10000 > 5000
			wantAllow:  false,
			wantReason: ReasonAboveMaxAlt,
		},
		{
			name: "unknown altitude with band",
			mutate: func(c *config.Options) {
				c.Above = 0
				c.Below = 40000
			},
			cand: func() *adsb.Aircraft {
				a := &adsb.Aircraft{Hex: "3c673a"}
				a.Lat, a.Lon = &insideLat, &insideLon
				return a // no altitude at all
			}(),
			wantAllow:  false,
			wantReason: ReasonUnknownAlt,
		},
		{
			name: "unknown altitude without band is fine",
			cand: func() *adsb.Aircraft {
				a := &adsb.Aircraft{Hex: "3c673a"}
				a.Lat, a.Lon = &insideLat, &insideLon
				return a
			}(),
			wantAllow:  true,
			wantReason: ReasonInsideRegion,
		},
		{
			name: "drop on ground",
			mutate: func(c *config.Options) {
				c.DropOnGround = true
			},
			cand: func() *adsb.Aircraft {
				a := obs("3c673a", "", insideLat, insideLon)
				a.SetAltBaroGround()
				return a
			}(),
			wantAllow:  false,
			wantReason: ReasonOnGround,
		},
		{
			name: "ground allowed when keep on ground",
			cand: func() *adsb.Aircraft {
				a := obs("3c673a", "", insideLat, insideLon)
				a.SetAltBaroGround()
				return a
			}(),
			wantAllow:  true,
			wantReason: ReasonInsideRegion,
		},
		{
			name: "ground counts as zero feet against the band",
			mutate: func(c *config.Options) {
				c.Above = 1000
			},
			cand: func() *adsb.Aircraft {
				a := obs("3c673a", "", insideLat, insideLon)
				a.SetAltBaroGround()
				return a
			}(),
			wantAllow:  false,
			wantReason: ReasonBelowMinAlt,
		},
		{
			name: "military only drops civilians",
			mutate: func(c *config.Options) {
				c.MilitaryOnly = true
			},
			cand:       obs("3c673a", "", insideLat, insideLon),
			wantAllow:  false,
			wantReason: ReasonNotMilitary,
		},
		{
			name: "military only keeps military",
			mutate: func(c *config.Options) {
				c.MilitaryOnly = true
			},
			cand: func() *adsb.Aircraft {
				a := obs("3c673a", "", insideLat, insideLon)
				a.DBFlags = adsb.Ptr(1)
				return a
			}(),
			wantAllow:  true,
			wantReason: ReasonInsideRegion,
		},
		{
			name: "military flag bit 2 is not military",
			mutate: func(c *config.Options) {
				c.MilitaryOnly = true
			},
			cand: func() *adsb.Aircraft {
				a := obs("3c673a", "", insideLat, insideLon)
				a.DBFlags = adsb.Ptr(2)
				return a
			}(),
			wantAllow:  false,
			wantReason: ReasonNotMilitary,
		},
		{
			name: "region containing the candidate only",
			mutate: func(c *config.Options) {
				c.Box = "51.4,-0.5,51.5,-0.4"
			},
			cand:       obs("3c673a", "", 52.5, -0.45),
			wantAllow:  false,
			wantReason: ReasonOutsideRegion,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := testFilter(t, tt.mutate)
			got := f.Decide(tt.cand)
			if got.Allow != tt.wantAllow || got.Reason != tt.wantReason {
				t.Errorf("Decide = allow=%v reason=%q, want allow=%v reason=%q",
					got.Allow, got.Reason, tt.wantAllow, tt.wantReason)
			}
		})
	}
}

func TestDecideCircleRegion(t *testing.T) {
	f := testFilter(t, func(c *config.Options) {
		c.Box = ""
		c.Circle = "51.5,-0.1,10"
	})
	tests := []struct {
		name string
		lat  float64
		lon  float64
		want bool
	}{
		{name: "centre", lat: 51.5, lon: -0.1, want: true},
		{name: "five miles out", lat: 51.57, lon: -0.1, want: true},
		{name: "fifty miles out", lat: 52.2, lon: -0.1, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := f.Decide(obs("3c673a", "", tt.lat, tt.lon))
			if got.Allow != tt.want {
				t.Errorf("allow = %v (reason %q), want %v", got.Allow, got.Reason, tt.want)
			}
		})
	}
}

func TestFormatDecision(t *testing.T) {
	tests := []struct {
		name string
		v    Verdict
		want string
	}{
		{
			name: "full line matches the documented sample",
			v: Verdict{
				Hex: "3c673a", Allow: true, Reason: ReasonInsideRegion,
				Alt: adsb.Ptr(12000.0), Lat: adsb.Ptr(51.47), Lon: adsb.Ptr(-0.45),
			},
			want: "hex=3c673a allow=true reason=inside-region alt=12000 pos=51.47000,-0.45000",
		},
		{
			name: "ground aircraft",
			v: Verdict{
				Hex: "abcdef", Allow: false, Reason: ReasonOnGround,
				OnGround: true, Alt: adsb.Ptr(0.0),
				Lat: adsb.Ptr(51.5), Lon: adsb.Ptr(-0.1),
			},
			want: "hex=abcdef allow=false reason=on-ground alt=ground pos=51.50000,-0.10000",
		},
		{
			name: "no data",
			v:    Verdict{Hex: "abcdef", Allow: false, Reason: ReasonNoPosition},
			want: "hex=abcdef allow=false reason=no-position alt=- pos=-",
		},
		{
			name: "fractional altitude",
			v: Verdict{
				Hex: "abcdef", Allow: true, Reason: ReasonAllowHex,
				Alt: adsb.Ptr(34999.5),
			},
			want: "hex=abcdef allow=true reason=allow-hex alt=34999.5 pos=-",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatDecision(tt.v); got != tt.want {
				t.Errorf("FormatDecision = %q, want %q", got, tt.want)
			}
		})
	}
}

// fakeClock is the injectable time source for tracker tests; it is
// guarded so the concurrency test can tick it while workers read it.
type fakeClock struct {
	mu sync.Mutex
	ts time.Time
}

func newClock() *fakeClock {
	return &fakeClock{ts: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ts
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ts = c.ts.Add(d)
}

func newTestTracker(t *testing.T, mutate func(*config.Options)) (*Tracker, *fakeClock) {
	t.Helper()
	clk := newClock()
	f := testFilter(t, mutate)
	tr := NewTracker(f)
	tr.now = clk.Now
	return tr, clk
}

func TestTrackerAllowCreatesAndForwards(t *testing.T) {
	tr, _ := newTestTracker(t, nil)
	v := tr.Observe(obs("3c673a", "BAW123", insideLat, insideLon))
	if !v.Allow || v.Reason != ReasonInsideRegion {
		t.Fatalf("Observe = %+v", v)
	}
	if !tr.CanForward("3c673a") {
		t.Error("allowed aircraft not forwardable")
	}
	if tr.Len() != 1 {
		t.Errorf("Len = %d, want 1", tr.Len())
	}
}

func TestTrackerDenyDoesNotCreateState(t *testing.T) {
	tr, _ := newTestTracker(t, nil)
	v := tr.Observe(obs("3c673a", "", outsideLat, outsideLon))
	if v.Allow {
		t.Fatal("outside observation allowed")
	}
	if tr.CanForward("3c673a") {
		t.Error("denied aircraft forwardable")
	}
	if tr.Len() != 0 {
		t.Errorf("Len = %d, want 0 (no state for denied aircraft)", tr.Len())
	}
}

func TestTrackerNoPositionUnknownAircraft(t *testing.T) {
	tr, _ := newTestTracker(t, nil)
	v := tr.Observe(&adsb.Aircraft{Hex: "3c673a"})
	if v.Allow || v.Reason != ReasonNoPosition {
		t.Fatalf("Observe = %+v", v)
	}
	if tr.Len() != 0 {
		t.Errorf("Len = %d, want 0", tr.Len())
	}
}

func TestTrackerFrozenStateSurvivesDeniedObservation(t *testing.T) {
	tr, _ := newTestTracker(t, nil)
	tr.Observe(obs("3c673a", "BAW123", insideLat, insideLon))

	// The aircraft is now outside: denied, and the held state must not
	// absorb the outside position.
	v := tr.Observe(obs("3c673a", "BAW123", outsideLat, outsideLon))
	if v.Allow || v.Reason != ReasonOutsideRegion {
		t.Fatalf("outside Observe = %+v", v)
	}
	if !tr.CanForward("3c673a") {
		t.Error("held aircraft should still forward within linger")
	}

	// A position-less follow-up is judged with the held inside position:
	// it stays allowed and does not refresh with outside data.
	v = tr.Observe(&adsb.Aircraft{Hex: "3c673a", Flight: "BAW123"})
	if !v.Allow {
		t.Fatalf("partial follow-up denied: %+v", v)
	}
	if v.Lat == nil || *v.Lat != insideLat {
		t.Errorf("follow-up judged at %v, want held inside latitude", v.Lat)
	}
}

func TestTrackerDenyHexDeletesHeldState(t *testing.T) {
	// An entry can only exist for a deny-hex address if the list became
	// effective after it was stored (or the state was injected); the next
	// observation must delete it, not merely stop forwarding.
	tr, _ := newTestTracker(t, func(c *config.Options) { c.DenyHex = []string{"3c673a"} })
	tr.entries["3c673a"] = &entry{
		ac:           obs("3c673a", "", insideLat, insideLon),
		lastAccepted: tr.now(),
		messages:     1,
	}
	v := tr.Observe(obs("3c673a", "", insideLat, insideLon))
	if v.Allow || v.Reason != ReasonDenyHex {
		t.Fatalf("Observe = %+v", v)
	}
	if tr.Len() != 0 {
		t.Errorf("Len = %d, want 0 (deny-hex deletes)", tr.Len())
	}
	if tr.CanForward("3c673a") {
		t.Error("deny-hex aircraft forwardable")
	}
}

func TestTrackerLingerExpiry(t *testing.T) {
	tr, clk := newTestTracker(t, func(c *config.Options) { c.Linger = 30 * time.Second })
	tr.Observe(obs("3c673a", "", insideLat, insideLon))

	clk.Add(29 * time.Second)
	if !tr.CanForward("3c673a") {
		t.Error("expired early (29s < 30s linger)")
	}
	clk.Add(2 * time.Second) // 31s total
	if tr.CanForward("3c673a") {
		t.Error("still forwardable after linger")
	}
	if got := tr.Snapshot().Aircraft; len(got) != 0 {
		t.Errorf("snapshot kept %d aircraft after linger", len(got))
	}
}

func TestTrackerLingerZeroKeepsEntries(t *testing.T) {
	tr, clk := newTestTracker(t, func(c *config.Options) { c.Linger = 0 })
	tr.Observe(obs("3c673a", "", insideLat, insideLon))
	clk.Add(24 * time.Hour)
	if !tr.CanForward("3c673a") {
		t.Error("linger 0 must disable time-based expiry")
	}
	if len(tr.Snapshot().Aircraft) != 1 {
		t.Error("entry vanished with linger 0")
	}
}

func TestTrackerPrune(t *testing.T) {
	tr, clk := newTestTracker(t, func(c *config.Options) { c.Linger = 10 * time.Second })
	tr.Observe(obs("3c673a", "", insideLat, insideLon))
	clk.Add(11 * time.Second)
	tr.Prune()
	if tr.Len() != 0 {
		t.Errorf("Len = %d after prune, want 0", tr.Len())
	}
}

func TestTrackerRefreshExtendsLease(t *testing.T) {
	tr, clk := newTestTracker(t, func(c *config.Options) { c.Linger = 30 * time.Second })
	tr.Observe(obs("3c673a", "", insideLat, insideLon))
	clk.Add(20 * time.Second)
	tr.Observe(obs("3c673a", "", insideLat, insideLon))
	clk.Add(20 * time.Second) // 40s since start, 20s since refresh
	if !tr.CanForward("3c673a") {
		t.Error("refreshed entry expired anyway")
	}
}

func TestTrackerCountMessage(t *testing.T) {
	tr, _ := newTestTracker(t, nil)
	tr.CountMessage("3c673a") // unknown hex: global count only
	if got := tr.Snapshot().Messages; got != 1 {
		t.Errorf("Messages = %d, want 1", got)
	}
	tr.Observe(obs("3c673a", "", insideLat, insideLon))
	tr.CountMessage("3c673a") // known hex: also counts on the entry
	doc := tr.Snapshot()
	if doc.Messages != 3 {
		t.Errorf("Messages = %d, want 3", doc.Messages)
	}
	if len(doc.Aircraft) != 1 {
		t.Fatalf("aircraft = %d, want 1", len(doc.Aircraft))
	}
	if got := doc.Aircraft[0]["messages"]; got != 2 {
		t.Errorf("aircraft messages = %v, want 2", got)
	}
}

func TestTrackerSetMessages(t *testing.T) {
	tr, _ := newTestTracker(t, nil)
	tr.Observe(obs("3c673a", "", insideLat, insideLon))
	tr.SetMessages(12345)
	if got := tr.Snapshot().Messages; got != 12345 {
		t.Errorf("Messages = %d, want 12345", got)
	}
}

func TestSnapshotShapeAndOrder(t *testing.T) {
	tr, clk := newTestTracker(t, nil)
	tr.Observe(obs("ffffff", "ZZZ999", insideLat, insideLon))
	clk.Add(3 * time.Second)
	tr.Observe(obs("000001", "AAA111", insideLat+0.1, insideLon))
	tr.SetMessages(999)

	doc := tr.Snapshot()
	if doc.Now != float64(clk.Now().Unix()) {
		t.Errorf("Now = %v, want unix seconds %v", doc.Now, clk.Now().Unix())
	}
	if doc.Messages != 999 {
		t.Errorf("Messages = %d", doc.Messages)
	}
	if len(doc.Aircraft) != 2 {
		t.Fatalf("aircraft = %d, want 2", len(doc.Aircraft))
	}
	if doc.Aircraft[0]["hex"] != "000001" || doc.Aircraft[1]["hex"] != "ffffff" {
		t.Errorf("not sorted by hex: %v, %v", doc.Aircraft[0]["hex"], doc.Aircraft[1]["hex"])
	}
	older := doc.Aircraft[1]
	if seen := older["seen"].(float64); seen != 3 {
		t.Errorf("seen = %v, want 3", seen)
	}
	if older["seen_pos"] == nil {
		t.Error("seen_pos missing for aircraft with a position")
	}
	if older["flight"] != "ZZZ999" {
		t.Errorf("flight = %v", older["flight"])
	}
	if _, ok := older["lat"]; !ok {
		t.Error("lat missing from snapshot")
	}
}

func TestSnapshotPreservesRawKeys(t *testing.T) {
	tr, _ := newTestTracker(t, nil)
	a := obs("3c673a", "BAW123", insideLat, insideLon)
	a.Raw = map[string]any{
		"hex": "3c673a", "category": "A3", "rssi": -3.5, "type": "A320",
	}
	tr.Observe(a)
	doc := tr.Snapshot()
	if len(doc.Aircraft) != 1 {
		t.Fatalf("aircraft = %d", len(doc.Aircraft))
	}
	m := doc.Aircraft[0]
	if m["category"] != "A3" || m["rssi"] != -3.5 || m["type"] != "A320" {
		t.Errorf("raw keys not preserved: %v", m)
	}
	if m["hex"] != "3c673a" {
		t.Errorf("hex = %v", m["hex"])
	}
}

func TestSnapshotSBSEntryBuildsFields(t *testing.T) {
	tr, _ := newTestTracker(t, nil)
	a := &adsb.Aircraft{Hex: "abcdef", Flight: "EZY833", Squawk: "7000"}
	a.Lat = &insideLat
	a.Lon = &insideLon
	a.SetAltBaroGround()
	tr.Observe(a)

	doc := tr.Snapshot()
	if len(doc.Aircraft) != 1 {
		t.Fatalf("aircraft = %d", len(doc.Aircraft))
	}
	m := doc.Aircraft[0]
	if m["alt_baro"] != "ground" {
		t.Errorf("alt_baro = %v, want ground", m["alt_baro"])
	}
	if m["flight"] != "EZY833" || m["squawk"] != "7000" {
		t.Errorf("typed fields missing: %v", m)
	}
	if m["lat"] != insideLat {
		t.Errorf("lat = %v", m["lat"])
	}
}

func TestSnapshotUnknownAltitudeOmitted(t *testing.T) {
	tr, _ := newTestTracker(t, nil)
	a := &adsb.Aircraft{Hex: "abcdef"}
	a.Lat = &insideLat
	a.Lon = &insideLon
	tr.Observe(a)
	m := tr.Snapshot().Aircraft[0]
	if _, ok := m["alt_baro"]; ok {
		t.Errorf("alt_baro = %v, want absent", m["alt_baro"])
	}
	if _, ok := m["seen_pos"]; !ok {
		t.Error("seen_pos missing for positioned aircraft")
	}
}

func TestSnapshotSeenPosOmittedWithoutPosition(t *testing.T) {
	tr, _ := newTestTracker(t, nil)
	tr.Observe(func() *adsb.Aircraft {
		a := &adsb.Aircraft{Hex: "3c673a"}
		return a
	}())
	// No position ever accepted: allow-hex keeps it, seen_pos absent.
	// (Without allow-hex such an aircraft is denied and never stored.)
	if tr.Len() != 0 {
		t.Skipf("entry not held: %d", tr.Len())
	}
	tr2, _ := newTestTracker(t, func(c *config.Options) { c.AllowHex = []string{"3c673a"} })
	tr2.Observe(&adsb.Aircraft{Hex: "3c673a"})
	m := tr2.Snapshot().Aircraft[0]
	if _, ok := m["seen_pos"]; ok {
		t.Errorf("seen_pos = %v, want absent", m["seen_pos"])
	}
	if _, ok := m["seen"]; !ok {
		t.Error("seen missing")
	}
}

func TestTrackerConcurrentUse(t *testing.T) {
	tr, clk := newTestTracker(t, func(c *config.Options) { c.Linger = 5 * time.Second })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				hex := fmt.Sprintf("%06x", i*100+j)
				a := obs(hex, "", insideLat, insideLon)
				tr.Observe(a)
				tr.CanForward(hex)
				tr.CountMessage(hex)
				tr.Snapshot()
				tr.Prune()
			}
		}(i)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	for {
		select {
		case <-done:
			return
		case <-time.After(time.Millisecond):
			clk.Add(time.Second)
		}
	}
}

func TestNewFilterNormalizesHexLists(t *testing.T) {
	f := testFilter(t, func(c *config.Options) {
		c.AllowHex = []string{"  AABBCC "}
		c.DenyHex = []string{"DDEEFF"}
	})
	if !f.allow["aabbcc"] || !f.deny["deadbe"] && f.deny["ddeeff"] != true {
		t.Errorf("hex lists not normalized: allow=%v deny=%v", f.allow, f.deny)
	}
	v := f.Decide(&adsb.Aircraft{Hex: "aabbcc"})
	if !v.Allow || v.Reason != ReasonAllowHex {
		t.Errorf("allow-hex after normalize: %+v", v)
	}
}

func TestFilterAccessors(t *testing.T) {
	f := testFilter(t, nil)
	if f.Region() == nil {
		t.Error("Region() nil")
	}
	if f.Linger() != 30*time.Second {
		t.Errorf("Linger() = %v", f.Linger())
	}
	if f.hasBand != (f.above > math.Inf(-1) || f.below < math.Inf(+1)) {
		t.Error("hasBand inconsistent")
	}
}
