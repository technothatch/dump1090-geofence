// Package geo implements the geometry the geofence filter needs: great
// circle distances, circle containment and point-in-polygon tests that stay
// correct when a region straddles the antimeridian.
//
// The antimeridian story is worth telling once: a polygon that crosses the
// date line (e.g. 170°E → 175°E → 175°W → 170°W) has vertices on both sides
// of ±180°, so a naive bounding box comes out as [-180, 180] — useless as a
// fast reject — and ray casting over the raw vertices shoots across the
// whole planet instead of across the small region. NewRing therefore
// "unwraps" the ring: every vertex longitude is shifted by a multiple of
// 360° so that it stays within 180° of its predecessor. The ring then lives
// in a continuous frame (say [170, 190]) and a query point is tested at
// lon, lon+360 and lon-360 so exactly one candidate lands in that frame.
package geo

import (
	"errors"
	"fmt"
	"math"
)

// EarthRadiusNM is the mean Earth radius in nautical miles
// (6371.0088 km, the IUGG mean radius, divided by 1.852 km/NM).
const EarthRadiusNM = 3440.0650131069736

// ErrRingTooFewVertices is returned when a ring has fewer than three
// distinct vertices.
var ErrRingTooFewVertices = errors.New("ring needs at least 3 distinct vertices")

func errMismatched(what string, a, b int) error {
	return fmt.Errorf("%s: length mismatch (%d vs %d)", what, a, b)
}

func errTooFewVertices(n int) error {
	return fmt.Errorf("%w, got %d", ErrRingTooFewVertices, n)
}

// ringEpsilon is the tolerance, in degrees, for treating a point as lying
// on a polygon edge. Roughly a metre at the equator.
const ringEpsilon = 1e-9

// Ring is a closed polygon ring in decimal degrees with longitudes unwrapped
// into a continuous frame (vertices may fall outside [-180, 180]).
type Ring struct {
	lats []float64
	lons []float64

	minLat, maxLat float64
	minLon, maxLon float64
}

// NewRing unwraps the supplied vertices and returns a Ring. At least three
// distinct vertices are required; the closing vertex may be omitted (it is
// added automatically, and an explicit duplicate closing vertex is dropped).
func NewRing(lat, lon []float64) (Ring, error) {
	if len(lat) != len(lon) {
		return Ring{}, errMismatched("ring vertex arrays", len(lat), len(lon))
	}
	// Drop an explicit closing vertex so the ring is always treated as open.
	if len(lat) >= 2 && lat[0] == lat[len(lat)-1] && lon[0] == lon[len(lon)-1] {
		lat = lat[:len(lat)-1]
		lon = lon[:len(lon)-1]
	}
	if len(lat) < 3 {
		return Ring{}, errTooFewVertices(len(lat))
	}

	lats := make([]float64, len(lat))
	lons := make([]float64, len(lon))
	copy(lats, lat)
	lons[0] = lon[0]
	for i := 1; i < len(lon); i++ {
		delta := normalizeLon(lon[i] - lon[i-1])
		lons[i] = lons[i-1] + delta
	}

	r := Ring{lats: lats, lons: lons}
	r.minLat, r.maxLat = lats[0], lats[0]
	r.minLon, r.maxLon = lons[0], lons[0]
	for i := 1; i < len(lats); i++ {
		if lats[i] < r.minLat {
			r.minLat = lats[i]
		}
		if lats[i] > r.maxLat {
			r.maxLat = lats[i]
		}
		if lons[i] < r.minLon {
			r.minLon = lons[i]
		}
		if lons[i] > r.maxLon {
			r.maxLon = lons[i]
		}
	}
	return r, nil
}

// MustRing is NewRing for tests and static data: it panics on invalid input.
func MustRing(lat, lon []float64) Ring {
	r, err := NewRing(lat, lon)
	if err != nil {
		panic(err)
	}
	return r
}

// Vertices returns the unwrapped vertex count.
func (r Ring) Vertices() int { return len(r.lats) }

// BBox returns the ring bounding box as minLat, minLon, maxLat, maxLon in
// the unwrapped frame: maxLon may exceed 180 for a ring that crosses the
// antimeridian (e.g. 170 → 190 rather than 170 → -170). That is precisely
// what makes the box usable as a fast reject.
func (r Ring) BBox() (minLat, minLon, maxLat, maxLon float64) {
	return r.minLat, r.minLon, r.maxLat, r.maxLon
}

// Contains reports whether the point lies inside the ring. Points exactly
// on an edge count as inside. The test is antimeridian-safe: the query
// longitude is tried at ±360° offsets so one candidate falls inside the
// ring's unwrapped frame.
func (r Ring) Contains(lat, lon float64) bool {
	if lat < r.minLat-ringEpsilon || lat > r.maxLat+ringEpsilon {
		return false
	}

	// Choose the offset that puts the point in the ring's lon frame.
	inside := false
	found := false
	for _, off := range []float64{0, 360, -360} {
		x := lon + off
		if x < r.minLon-ringEpsilon || x > r.maxLon+ringEpsilon {
			continue
		}
		found = true
		if r.containsUnwrapped(lat, x) {
			inside = true
		}
	}
	return found && inside
}

// containsUnwrapped runs the boundary check plus the ray-casting test for a
// point whose longitude already sits in the ring's frame.
func (r Ring) containsUnwrapped(lat, lon float64) bool {
	n := len(r.lats)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		if pointOnSegment(lon, lat, r.lons[i], r.lats[i], r.lons[j], r.lats[j]) {
			return true
		}
	}

	// Crossing-count ray cast: shoot a ray towards +lon and count edge
	// crossings; odd means inside. Works for concave rings.
	crossings := 0
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		yi, yj := r.lats[i], r.lats[j]
		if (yi > lat) == (yj > lat) {
			continue
		}
		xi, xj := r.lons[i], r.lons[j]
		x := xi + (lat-yi)*(xj-xi)/(yj-yi)
		if x > lon {
			crossings++
		}
	}
	return crossings%2 == 1
}

// HaversineNM returns the great-circle distance between two points in
// nautical miles. The haversine form is inherently antimeridian-safe: the
// sin²(Δlon/2) term is invariant when Δlon wraps by 360°.
func HaversineNM(lat1, lon1, lat2, lon2 float64) float64 {
	const deg2rad = math.Pi / 180
	dLat := (lat2 - lat1) * deg2rad
	dLon := (lon2 - lon1) * deg2rad
	sinLat := math.Sin(dLat / 2)
	sinLon := math.Sin(dLon / 2)
	h := sinLat*sinLat + math.Cos(lat1*deg2rad)*math.Cos(lat2*deg2rad)*sinLon*sinLon
	return 2 * EarthRadiusNM * math.Asin(math.Min(1, math.Sqrt(h)))
}

// normalizeLon maps a longitude difference into (-180, 180], the short way
// around the globe.
func normalizeLon(lon float64) float64 {
	lon = math.Mod(lon, 360)
	if lon > 180 {
		lon -= 360
	} else if lon <= -180 {
		lon += 360
	}
	return lon
}

// pointOnSegment reports whether (px, py) lies on the segment a→b within
// ringEpsilon.
func pointOnSegment(px, py, ax, ay, bx, by float64) bool {
	cross := (bx-ax)*(py-ay) - (by-ay)*(px-ax)
	if math.Abs(cross) > ringEpsilon {
		return false
	}
	if math.Min(ax, bx)-ringEpsilon > px || px > math.Max(ax, bx)+ringEpsilon {
		return false
	}
	if math.Min(ay, by)-ringEpsilon > py || py > math.Max(ay, by)+ringEpsilon {
		return false
	}
	return true
}
