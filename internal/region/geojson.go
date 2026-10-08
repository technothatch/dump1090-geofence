package region

import (
	"encoding/json"
	"fmt"
	"os"
)

// geoJSON root types. Only what a fence polygon file realistically
// contains is modelled: a FeatureCollection of (Multi)Polygons. Points,
// LineStrings and friends are rejected with a clear error — a line does not
// enclose anything.
type geoJSONRoot struct {
	Type        string           `json:"type"`
	Features    []geoJSONFeature `json:"features"`
	Geometry    *geoJSONGeometry `json:"geometry"`
	Coordinates json.RawMessage  `json:"coordinates"`
}

type geoJSONFeature struct {
	Type     string          `json:"type"`
	Geometry geoJSONGeometry `json:"geometry"`
}

type geoJSONGeometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

// LoadGeoJSON reads a GeoJSON file and returns a Polygon region covering
// its outer rings (holes excluded). A FeatureCollection with several
// polygon features becomes a union; a lone Feature or bare geometry is
// accepted as a convenience.
func LoadGeoJSON(path string) (Region, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read geojson: %w", err)
	}
	poly, err := ParseGeoJSON(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return poly, nil
}

// ParseGeoJSON parses GeoJSON bytes into a Polygon region.
func ParseGeoJSON(data []byte) (Polygon, error) {
	var root geoJSONRoot
	if err := json.Unmarshal(data, &root); err != nil {
		return Polygon{}, fmt.Errorf("invalid geojson: %w", err)
	}

	var geoms []geoJSONGeometry
	switch root.Type {
	case "FeatureCollection":
		if len(root.Features) == 0 {
			return Polygon{}, fmt.Errorf("feature collection contains no features")
		}
		for i, f := range root.Features {
			if f.Geometry.Type == "" {
				return Polygon{}, fmt.Errorf("feature %d has no geometry", i)
			}
			geoms = append(geoms, f.Geometry)
		}
	case "Feature":
		if root.Geometry == nil {
			return Polygon{}, fmt.Errorf("feature has no geometry")
		}
		geoms = []geoJSONGeometry{*root.Geometry}
	case "Polygon", "MultiPolygon":
		geoms = []geoJSONGeometry{{Type: root.Type, Coordinates: root.Coordinates}}
	default:
		// Bare geometry of any kind (Polygon, MultiPolygon, Point, ...);
		// geometryPolygonSets rejects whatever does not enclose a region.
		geoms = []geoJSONGeometry{{Type: root.Type, Coordinates: root.Coordinates}}
	}

	var (
		p     Polygon
		polys int
		verts int
	)
	for i, g := range geoms {
		sets, err := geometryPolygonSets(g, fmt.Sprintf("geometry %d", i))
		if err != nil {
			return Polygon{}, err
		}
		for _, s := range sets {
			p.polygons = append(p.polygons, s)
			verts += s.outer.Vertices()
			polys++
		}
	}
	if polys == 0 {
		return Polygon{}, fmt.Errorf("no polygons found in geojson")
	}
	p.label = fmt.Sprintf("polygon(%d rings, %d vertices)", polys, verts)
	return p, nil
}

// geometryPolygonSets converts one geometry into polygon sets.
func geometryPolygonSets(g geoJSONGeometry, label string) ([]polygonSet, error) {
	switch g.Type {
	case "Polygon":
		var rings [][][]float64
		if err := json.Unmarshal(g.Coordinates, &rings); err != nil {
			return nil, fmt.Errorf("%s: bad Polygon coordinates: %w", label, err)
		}
		set, err := parseRings(rings, label)
		if err != nil {
			return nil, err
		}
		return []polygonSet{set}, nil
	case "MultiPolygon":
		var polys [][][][]float64
		if err := json.Unmarshal(g.Coordinates, &polys); err != nil {
			return nil, fmt.Errorf("%s: bad MultiPolygon coordinates: %w", label, err)
		}
		if len(polys) == 0 {
			return nil, fmt.Errorf("%s: multipolygon is empty", label)
		}
		sets := make([]polygonSet, 0, len(polys))
		for i, rings := range polys {
			set, err := parseRings(rings, fmt.Sprintf("%s polygon %d", label, i))
			if err != nil {
				return nil, err
			}
			sets = append(sets, set)
		}
		return sets, nil
	default:
		return nil, fmt.Errorf("%s: geometry type %q does not enclose a region (want Polygon or MultiPolygon)", label, g.Type)
	}
}
