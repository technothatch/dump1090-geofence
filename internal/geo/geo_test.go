package geo

import (
	"errors"
	"math"
	"testing"
)

func TestNewRingValidation(t *testing.T) {
	tests := []struct {
		name    string
		lat     []float64
		lon     []float64
		wantErr error
		wantMsg string
	}{
		{
			name: "closed ring is accepted and closing vertex dropped",
			lat:  []float64{51, 51, 52, 52, 51},
			lon:  []float64{-1, 0, 0, -1, -1},
		},
		{
			name: "open ring is accepted",
			lat:  []float64{51, 51, 52, 52},
			lon:  []float64{-1, 0, 0, -1},
		},
		{
			name:    "two vertices rejected",
			lat:     []float64{51, 52},
			lon:     []float64{-1, 0},
			wantErr: ErrRingTooFewVertices,
		},
		{
			name:    "length mismatch rejected",
			lat:     []float64{51, 52, 53},
			lon:     []float64{-1, 0},
			wantMsg: "ring vertex arrays: length mismatch (3 vs 2)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := NewRing(tt.lat, tt.lon)
			if tt.wantErr != nil || tt.wantMsg != "" {
				if err == nil {
					t.Fatal("NewRing() error = nil, want an error")
				}
				if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
					t.Fatalf("NewRing() error = %v, want %v", err, tt.wantErr)
				}
				if tt.wantMsg != "" && err.Error() != tt.wantMsg {
					t.Fatalf("NewRing() error = %q, want %q", err, tt.wantMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewRing() unexpected error: %v", err)
			}
			wantVerts := len(tt.lat)
			if tt.lat[0] == tt.lat[len(tt.lat)-1] && tt.lon[0] == tt.lon[len(tt.lon)-1] {
				wantVerts--
			}
			if r.Vertices() != wantVerts {
				t.Errorf("Vertices() = %d, want %d", r.Vertices(), wantVerts)
			}
		})
	}
}

func TestRingContainsCardinalDirections(t *testing.T) {
	// Unit square centred on the origin: (-1,-1) .. (1,1).
	r := MustRing([]float64{-1, -1, 1, 1}, []float64{-1, 1, 1, -1})
	tests := []struct {
		name string
		lat  float64
		lon  float64
		want bool
	}{
		{name: "centre", lat: 0, lon: 0, want: true},
		{name: "inside near corner", lat: 0.9, lon: -0.9, want: true},
		{name: "north outside", lat: 1.5, lon: 0, want: false},
		{name: "south outside", lat: -1.5, lon: 0, want: false},
		{name: "east outside", lat: 0, lon: 1.5, want: false},
		{name: "west outside", lat: 0, lon: -1.5, want: false},
		{name: "diagonal outside", lat: 2, lon: 2, want: false},
		{name: "on west edge", lat: 0, lon: -1, want: true},
		{name: "on north edge", lat: 1, lon: 0.5, want: true},
		{name: "on vertex", lat: 1, lon: 1, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Contains(tt.lat, tt.lon); got != tt.want {
				t.Errorf("Contains(%v, %v) = %v, want %v", tt.lat, tt.lon, got, tt.want)
			}
		})
	}
}

func TestRingContainsConcavePolygon(t *testing.T) {
	// L-shape: notch in the upper-right quadrant must count as outside
	// while both arms count as inside.
	r := MustRing(
		[]float64{0, 0, 1, 1, 2, 2},
		[]float64{0, 2, 2, 1, 1, 0},
	)
	tests := []struct {
		name string
		lat  float64
		lon  float64
		want bool
	}{
		{name: "bottom arm", lat: 0.5, lon: 1.5, want: true},
		{name: "left arm", lat: 1.5, lon: 0.5, want: true},
		{name: "inside the crook", lat: 1.8, lon: 1.8, want: false},
		{name: "notch corner just outside", lat: 1.05, lon: 1.05, want: false},
		{name: "just inside the crook bend", lat: 0.95, lon: 0.95, want: true},
		{name: "far outside", lat: -0.5, lon: 2.5, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Contains(tt.lat, tt.lon); got != tt.want {
				t.Errorf("Contains(%v, %v) = %v, want %v", tt.lat, tt.lon, got, tt.want)
			}
		})
	}
}

func TestRingAntimeridianBBoxAndContainment(t *testing.T) {
	// A region straddling the date line: 170°E → 170°W → 170°W → 170°E.
	r := MustRing(
		[]float64{10, 10, 20, 20},
		[]float64{170, -170, -170, 170},
	)
	minLat, minLon, maxLat, maxLon := r.BBox()
	if minLat != 10 || maxLat != 20 {
		t.Errorf("lat bbox = [%v, %v], want [10, 20]", minLat, maxLat)
	}
	if minLon != 170 || maxLon != 190 {
		t.Errorf("lon bbox = [%v, %v], want unwrapped [170, 190]", minLon, maxLon)
	}

	tests := []struct {
		name string
		lat  float64
		lon  float64
		want bool
	}{
		{name: "centre expressed as positive lon", lat: 15, lon: 177.5, want: true},
		{name: "centre expressed across the date line", lat: 15, lon: -177.5, want: true},
		{name: "far side of the date line outside", lat: 15, lon: 160, want: false},
		{name: "east of region outside", lat: 15, lon: -160, want: false},
		{name: "inside near wrapped edge", lat: 15, lon: -176, want: true},
		{name: "latitude outside", lat: 30, lon: 177.5, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Contains(tt.lat, tt.lon); got != tt.want {
				t.Errorf("Contains(%v, %v) = %v, want %v", tt.lat, tt.lon, got, tt.want)
			}
		})
	}
}

func TestRingContainsSouthernHemisphereBox(t *testing.T) {
	r := MustRing(
		[]float64{-34, -34, -33, -33},
		[]float64{-59, -58, -58, -59},
	)
	if !r.Contains(-33.5, -58.5) {
		t.Error("point inside Buenos Aires box reported outside")
	}
	if r.Contains(-35, -58.5) {
		t.Error("point south of box reported inside")
	}
}

func TestHaversineNMKnownDistances(t *testing.T) {
	tests := []struct {
		name   string
		lat1   float64
		lon1   float64
		lat2   float64
		lon2   float64
		wantNM float64
		tolNM  float64
	}{
		{
			name: "one degree of latitude",
			lat1: 0, lon1: 0, lat2: 1, lon2: 0,
			wantNM: 60.04, tolNM: 0.01,
		},
		{
			name: "ten degrees of latitude along a meridian",
			lat1: 10, lon1: 20, lat2: 20, lon2: 20,
			wantNM: 600.4, tolNM: 0.05,
		},
		{
			name: "one degree of longitude at the equator",
			lat1: 0, lon1: 0, lat2: 0, lon2: 1,
			wantNM: 60.04, tolNM: 0.01,
		},
		{
			name: "antipodal half circumference along the equator",
			lat1: 0, lon1: 0, lat2: 0, lon2: 180,
			wantNM: 10807.28, tolNM: 0.1,
		},
		{
			name: "identical points",
			lat1: 51.5, lon1: -0.1, lat2: 51.5, lon2: -0.1,
			wantNM: 0, tolNM: 1e-9,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HaversineNM(tt.lat1, tt.lon1, tt.lat2, tt.lon2)
			if math.Abs(got-tt.wantNM) > tt.tolNM {
				t.Errorf("HaversineNM() = %.4f NM, want %.4f ± %.4f", got, tt.wantNM, tt.tolNM)
			}
		})
	}
}

func TestHaversineNMAntimeridianSafe(t *testing.T) {
	// The short way across the date line (~54 NM at lat 15) must not be
	// confused with the long way round the planet (~19500 NM).
	short := HaversineNM(15, 179.9, 15, -179.9)
	if short > 60 {
		t.Errorf("short crossing = %.2f NM, want well under 60 NM", short)
	}
	// Symmetry: swapping the endpoints must not change the distance.
	swapped := HaversineNM(15, -179.9, 15, 179.9)
	if math.Abs(short-swapped) > 1e-9 {
		t.Errorf("asymmetric across antimeridian: %.6f vs %.6f", short, swapped)
	}
}

func TestRingIsPointInPolygonAntimeridianEquivalence(t *testing.T) {
	// The same physical polygon expressed with unwrapped vertices (going
	// the long way in raw degrees must still classify identically).
	wrapped := MustRing(
		[]float64{-10, -10, 10, 10},
		[]float64{175, -175, -175, 175},
	)
	for _, pt := range []struct{ lat, lon float64 }{
		{0, 180}, {0, -180}, {0, 179}, {0, -179}, {0, 170}, {0, -170}, {15, 178},
	} {
		inside := wrapped.Contains(pt.lat, pt.lon)
		// 0/180 and neighbours inside the band; 170/-170 outside.
		want := math.Abs(pt.lon) >= 175 || math.Abs(pt.lon) == 180
		if pt.lat == 15 {
			want = false
		}
		if inside != want {
			t.Errorf("Contains(%v, %v) = %v, want %v", pt.lat, pt.lon, inside, want)
		}
	}
}
