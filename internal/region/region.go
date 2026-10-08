// Package region turns the --box / --circle specifications into a Region
// the filter can test aircraft positions against.
//
// A region is either a polygon (from a GeoJSON FeatureCollection file or a
// "minLat,minLon,maxLat,maxLon" bounds string) or a circle (from a
// "lat,lon,radius_nm" string, mirroring readsb's ?circle= query
// parameter). Polygon containment uses geo.Ring, which unwraps longitudes
// so regions crossing the antimeridian classify correctly.
package region

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/technothatch/dump1090-geofence/internal/geo"
)

// Region answers "is this position inside the share area?".
type Region interface {
	// Contains reports whether the decimal-degree point lies inside.
	Contains(lat, lon float64) bool
	// Describe is a short, stable description for logs and dry-run output.
	Describe() string
}

// Circle is a great-circle region of a fixed radius in nautical miles,
// centred on (Lat, Lon). Distances use geo.HaversineNM, which is
// antimeridian-safe by construction.
type Circle struct {
	Lat      float64
	Lon      float64
	RadiusNM float64
}

// NewCircle parses a "lat,lon,radius_nm" specification.
func NewCircle(spec string) (Circle, error) {
	parts := strings.Split(spec, ",")
	if len(parts) != 3 {
		return Circle{}, fmt.Errorf("circle must be lat,lon,radius_nm, got %q", spec)
	}
	nums := make([]float64, 3)
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return Circle{}, fmt.Errorf("circle component %q: %w", strings.TrimSpace(p), err)
		}
		nums[i] = f
	}
	c := Circle{Lat: nums[0], Lon: nums[1], RadiusNM: nums[2]}
	if c.Lat < -90 || c.Lat > 90 {
		return Circle{}, fmt.Errorf("circle latitude %g out of range [-90, 90]", c.Lat)
	}
	if c.Lon < -180 || c.Lon > 180 {
		return Circle{}, fmt.Errorf("circle longitude %g out of range [-180, 180]", c.Lon)
	}
	if c.RadiusNM <= 0 {
		return Circle{}, fmt.Errorf("circle radius must be positive, got %g", c.RadiusNM)
	}
	return c, nil
}

// Contains reports whether the point is within RadiusNM of the centre.
func (c Circle) Contains(lat, lon float64) bool {
	return geo.HaversineNM(c.Lat, c.Lon, lat, lon) <= c.RadiusNM
}

// Describe implements Region.
func (c Circle) Describe() string {
	return fmt.Sprintf("circle(lat=%g,lon=%g,r=%gnm)", c.Lat, c.Lon, c.RadiusNM)
}

// Polygon is one or more polygons (outer rings with optional holes); a
// MultiPolygon GeoJSON geometry becomes a Polygon whose outer rings
// combine with OR. A point is inside when it is in some outer ring and not
// in that polygon's holes.
type Polygon struct {
	polygons []polygonSet
	label    string
}

type polygonSet struct {
	outer geo.Ring
	holes []geo.Ring
}

// NewRect builds a rectangular polygon from minLat,minLon,maxLat,maxLon.
// minLon > maxLon is allowed and denotes a box crossing the antimeridian
// (e.g. 170,-170); geo.NewRing unwraps it.
func NewRect(minLat, minLon, maxLat, maxLon float64) (Polygon, error) {
	if minLat > maxLat {
		return Polygon{}, fmt.Errorf("box: minLat %g > maxLat %g", minLat, maxLat)
	}
	if minLat < -90 || maxLat > 90 {
		return Polygon{}, fmt.Errorf("box latitudes %g..%g out of range [-90, 90]", minLat, maxLat)
	}
	if minLon < -180 || maxLon > 180 {
		return Polygon{}, fmt.Errorf("box longitudes %g..%g out of range [-180, 180]", minLon, maxLon)
	}
	if minLat == maxLat || minLon == maxLon {
		return Polygon{}, fmt.Errorf("box is degenerate: %g,%g,%g,%g", minLat, minLon, maxLat, maxLon)
	}
	r, err := geo.NewRing(
		[]float64{minLat, minLat, maxLat, maxLat},
		[]float64{minLon, maxLon, maxLon, minLon},
	)
	if err != nil {
		return Polygon{}, fmt.Errorf("box: %w", err)
	}
	return Polygon{
		polygons: []polygonSet{{outer: r}},
		label:    fmt.Sprintf("box(%g,%g,%g,%g)", minLat, minLon, maxLat, maxLon),
	}, nil
}

// parseRings builds one polygon set from GeoJSON-style coordinate rings:
// the first ring is the outer boundary, the rest are holes.
func parseRings(rings [][][]float64, label string) (polygonSet, error) {
	if len(rings) == 0 {
		return polygonSet{}, fmt.Errorf("%s: polygon has no rings", label)
	}
	outer, err := ringFromPositions(rings[0], label, "outer")
	if err != nil {
		return polygonSet{}, err
	}
	set := polygonSet{outer: outer}
	for i, hole := range rings[1:] {
		h, err := ringFromPositions(hole, label, fmt.Sprintf("hole %d", i))
		if err != nil {
			return polygonSet{}, err
		}
		set.holes = append(set.holes, h)
	}
	return set, nil
}

// ringFromPositions converts a GeoJSON linear ring — a list of [lon, lat]
// positions — into a geo.Ring. NewRing enforces the minimum vertex count
// and unwraps longitudes across the antimeridian.
func ringFromPositions(pos [][]float64, label, what string) (geo.Ring, error) {
	lats := make([]float64, 0, len(pos))
	lons := make([]float64, 0, len(pos))
	for i, p := range pos {
		if len(p) != 2 {
			return geo.Ring{}, fmt.Errorf("%s: %s ring position %d must be [lon, lat], got %d values", label, what, i, len(p))
		}
		lons = append(lons, p[0])
		lats = append(lats, p[1])
	}
	r, err := geo.NewRing(lats, lons)
	if err != nil {
		return geo.Ring{}, fmt.Errorf("%s: %s ring: %w", label, what, err)
	}
	return r, nil
}

// Contains implements Region.
func (p Polygon) Contains(lat, lon float64) bool {
	for _, set := range p.polygons {
		if !set.outer.Contains(lat, lon) {
			continue
		}
		holed := false
		for _, h := range set.holes {
			if h.Contains(lat, lon) {
				holed = true
				break
			}
		}
		if !holed {
			return true
		}
	}
	return false
}

// Describe implements Region.
func (p Polygon) Describe() string { return p.label }

// Build constructs the configured region from exactly one non-empty spec.
func Build(box, circle string) (Region, error) {
	if box != "" && circle != "" {
		return nil, fmt.Errorf("both box and circle configured; use exactly one")
	}
	switch {
	case circle != "":
		return NewCircle(circle)
	case box != "":
		return buildBox(box)
	default:
		return nil, fmt.Errorf("no region configured")
	}
}

// buildBox accepts either a bounds string or a path to a GeoJSON file.
func buildBox(spec string) (Region, error) {
	if strings.Contains(spec, ",") {
		parts := strings.Split(spec, ",")
		if len(parts) != 4 {
			return nil, fmt.Errorf("box must be a GeoJSON path or minLat,minLon,maxLat,maxLon, got %q", spec)
		}
		nums := make([]float64, 4)
		for i, p := range parts {
			f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if err != nil {
				return nil, fmt.Errorf("box component %q: %w", strings.TrimSpace(p), err)
			}
			nums[i] = f
		}
		return NewRect(nums[0], nums[1], nums[2], nums[3])
	}
	return LoadGeoJSON(spec)
}
