package region

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildBoxBounds(t *testing.T) {
	r, err := Build("51.0,-1.0,52.0,0.5", "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	tests := []struct {
		name string
		lat  float64
		lon  float64
		want bool
	}{
		{name: "inside London", lat: 51.5, lon: -0.1, want: true},
		{name: "outside north", lat: 52.5, lon: 0, want: false},
		{name: "outside west", lat: 51.5, lon: -1.5, want: false},
		{name: "on corner", lat: 52.0, lon: 0.5, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Contains(tt.lat, tt.lon); got != tt.want {
				t.Errorf("Contains(%v, %v) = %v, want %v", tt.lat, tt.lon, got, tt.want)
			}
		})
	}
	if d := r.Describe(); !strings.HasPrefix(d, "box(") {
		t.Errorf("Describe() = %q", d)
	}
}

func TestBuildBoxCrossingAntimeridian(t *testing.T) {
	// minLon > maxLon means the box straddles the date line.
	r, err := Build("10,170,20,-170", "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	tests := []struct {
		name string
		lat  float64
		lon  float64
		want bool
	}{
		{name: "centre positive", lat: 15, lon: 179, want: true},
		{name: "centre wrapped", lat: 15, lon: -179, want: true},
		{name: "west of box", lat: 15, lon: 160, want: false},
		{name: "east of box", lat: 15, lon: -160, want: false},
		{name: "above box", lat: 25, lon: 179, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Contains(tt.lat, tt.lon); got != tt.want {
				t.Errorf("Contains(%v, %v) = %v, want %v", tt.lat, tt.lon, got, tt.want)
			}
		})
	}
}

func TestBuildBoxErrors(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		wantSub string
	}{
		{name: "inverted latitudes", spec: "52,-1,51,0", wantSub: "minLat"},
		{name: "latitude out of range", spec: "-95,-1,51,0", wantSub: "out of range"},
		{name: "longitude out of range", spec: "51,-181,52,0", wantSub: "out of range"},
		{name: "degenerate", spec: "51,-1,51,0", wantSub: "degenerate"},
		{name: "three components", spec: "51,-1,0", wantSub: "GeoJSON path or"},
		{name: "non-numeric", spec: "a,b,c,d", wantSub: "box component"},
		{name: "empty", spec: "", wantSub: "no region configured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Build(tt.spec, "")
			if err == nil {
				t.Fatalf("Build(%q) succeeded, want error", tt.spec)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want substring %q", err, tt.wantSub)
			}
		})
	}
}

func TestNewCircle(t *testing.T) {
	c, err := NewCircle("51.5,-0.1,40")
	if err != nil {
		t.Fatalf("NewCircle: %v", err)
	}
	if c.Lat != 51.5 || c.Lon != -0.1 || c.RadiusNM != 40 {
		t.Errorf("circle = %+v", c)
	}
	tests := []struct {
		name string
		lat  float64
		lon  float64
		want bool
	}{
		{name: "centre", lat: 51.5, lon: -0.1, want: true},
		{name: "nearby point", lat: 51.7, lon: -0.1, want: true},
		{name: "outside radius", lat: 53, lon: -0.1, want: false},
		{name: "far away", lat: 48.85, lon: 2.35, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.Contains(tt.lat, tt.lon); got != tt.want {
				t.Errorf("Contains(%v, %v) = %v, want %v", tt.lat, tt.lon, got, tt.want)
			}
		})
	}
	if d := c.Describe(); !strings.HasPrefix(d, "circle(") {
		t.Errorf("Describe() = %q", d)
	}
}

func TestNewCircleErrors(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		wantSub string
	}{
		{name: "two components", spec: "51.5,-0.1", wantSub: "lat,lon,radius_nm"},
		{name: "four components", spec: "51.5,-0.1,40,50", wantSub: "lat,lon,radius_nm"},
		{name: "non-numeric", spec: "here,there,40", wantSub: "circle component"},
		{name: "latitude out of range", spec: "95,-0.1,40", wantSub: "latitude"},
		{name: "longitude out of range", spec: "51.5,200,40", wantSub: "longitude"},
		{name: "zero radius", spec: "51.5,-0.1,0", wantSub: "radius must be positive"},
		{name: "negative radius", spec: "51.5,-0.1,-5", wantSub: "radius must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewCircle(tt.spec)
			if err == nil {
				t.Fatalf("NewCircle(%q) succeeded, want error", tt.spec)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want substring %q", err, tt.wantSub)
			}
		})
	}
}

func TestCircleCrossingAntimeridian(t *testing.T) {
	c, err := NewCircle("15,179.8,30")
	if err != nil {
		t.Fatalf("NewCircle: %v", err)
	}
	if !c.Contains(15, -179.9) {
		t.Error("point just across the date line reported outside")
	}
	if c.Contains(15, 170) {
		t.Error("point 10 degrees away reported inside")
	}
}

func TestBuildRejectsBothRegions(t *testing.T) {
	if _, err := Build("51,-1,52,0", "51.5,-0.1,40"); err == nil {
		t.Fatal("Build with both specs succeeded, want error")
	}
}

func TestBuildGeoJSONFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fence.geojson")
	const fc = `{
	  "type": "FeatureCollection",
	  "features": [
	    {
	      "type": "Feature",
	      "properties": {"name": "share area"},
	      "geometry": {
	        "type": "Polygon",
	        "coordinates": [[
	          [-1.0, 51.0], [0.5, 51.0], [0.5, 52.0], [-1.0, 52.0], [-1.0, 51.0]
	        ]]
	      }
	    }
	  ]
	}`
	if err := os.WriteFile(path, []byte(fc), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	r, err := Build(path, "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !r.Contains(51.5, -0.1) {
		t.Error("inside point reported outside")
	}
	if r.Contains(60, -0.1) {
		t.Error("outside point reported inside")
	}
	if !strings.HasPrefix(r.Describe(), "polygon(") {
		t.Errorf("Describe() = %q", r.Describe())
	}
}

func TestBuildGeoJSONMissingFile(t *testing.T) {
	if _, err := Build(filepath.Join(t.TempDir(), "nope.geojson"), ""); err == nil {
		t.Error("Build(missing file) succeeded, want error")
	}
}

func TestParseGeoJSONShapes(t *testing.T) {
	tests := []struct {
		name    string
		geo     string
		wantIn  [2]float64
		wantOut [2]float64
	}{
		{
			name: "multipolygon union",
			geo: `{"type":"FeatureCollection","features":[
				{"type":"Feature","geometry":{"type":"MultiPolygon","coordinates":[
					[[[0,0],[1,0],[1,1],[0,1],[0,0]]],
					[[[5,5],[6,5],[6,6],[5,6],[5,5]]]
				]}}
			]}`,
			wantIn:  [2]float64{0.5, 0.5},
			wantOut: [2]float64{3, 3},
		},
		{
			name: "polygon with a hole",
			geo: `{"type":"Feature","geometry":{"type":"Polygon","coordinates":[
				[[0,0],[4,0],[4,4],[0,4],[0,0]],
				[[1,1],[3,1],[3,3],[1,3],[1,1]]
			]}}`,
			wantIn:  [2]float64{0.5, 0.5},
			wantOut: [2]float64{2, 2},
		},
		{
			name:    "bare geometry",
			geo:     `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1],[0,0]]]}`,
			wantIn:  [2]float64{0.5, 0.5},
			wantOut: [2]float64{2, 0.5},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := ParseGeoJSON([]byte(tt.geo))
			if err != nil {
				t.Fatalf("ParseGeoJSON: %v", err)
			}
			if !r.Contains(tt.wantIn[0], tt.wantIn[1]) {
				t.Errorf("point %v should be inside", tt.wantIn)
			}
			if r.Contains(tt.wantOut[0], tt.wantOut[1]) {
				t.Errorf("point %v should be outside", tt.wantOut)
			}
		})
	}
}

func TestParseGeoJSONErrors(t *testing.T) {
	tests := []struct {
		name    string
		geo     string
		wantSub string
	}{
		{name: "not json", geo: `nope`, wantSub: "invalid geojson"},
		{name: "unsupported root type", geo: `{"type":"Point","coordinates":[0,0]}`, wantSub: "does not enclose"},
		{name: "line geometry", geo: `{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"LineString","coordinates":[[0,0],[1,1]]}}]}`, wantSub: "does not enclose"},
		{name: "empty collection", geo: `{"type":"FeatureCollection","features":[]}`, wantSub: "no features"},
		{name: "feature without geometry", geo: `{"type":"FeatureCollection","features":[{"type":"Feature"}]}`, wantSub: "no geometry"},
		{name: "empty polygon", geo: `{"type":"Polygon","coordinates":[]}`, wantSub: "no rings"},
		{name: "too few vertices", geo: `{"type":"Polygon","coordinates":[[[0,0],[1,1]]]}`, wantSub: "at least 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseGeoJSON([]byte(tt.geo))
			if err == nil {
				t.Fatalf("ParseGeoJSON(%s) succeeded, want error", tt.geo)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want substring %q", err, tt.wantSub)
			}
		})
	}
}
