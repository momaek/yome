package layout

import (
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/momaek/yome/internal/geom"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func TestParsePathDAbsoluteAndRelativeAgree(t *testing.T) {
	// The same square, drawn absolute and relative.
	abs, err := ParsePathD("M 10 10 L 20 10 L 20 20 L 10 20 Z")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := ParsePathD("m 10 10 l 10 0 l 0 10 l -10 0 z")
	if err != nil {
		t.Fatal(err)
	}
	assertSameStrokes(t, rel, abs)
}

func TestParsePathDCommands(t *testing.T) {
	tests := []struct {
		name string
		d    string
		want []geom.Point // the polyline's endpoints, sampled
	}{{
		name: "H and V",
		d:    "M 0 0 H 10 V 5",
		want: []geom.Point{{X: 0, Y: 0}, {X: 10, Y: 0}, {X: 10, Y: 5}},
	}, {
		name: "h and v are relative",
		d:    "M 5 5 h 10 v -5",
		want: []geom.Point{{X: 5, Y: 5}, {X: 15, Y: 5}, {X: 15, Y: 0}},
	}, {
		name: "Z closes back to the subpath start",
		d:    "M 0 0 L 10 0 L 10 10 Z",
		want: []geom.Point{{X: 0, Y: 0}, {X: 10, Y: 0}, {X: 10, Y: 10}, {X: 0, Y: 0}},
	}, {
		name: "implicit lineto repeats after M",
		d:    "M 0 0 10 0 10 10",
		want: []geom.Point{{X: 0, Y: 0}, {X: 10, Y: 0}, {X: 10, Y: 10}},
	}, {
		name: "implicit repeat of L",
		d:    "M 0 0 L 10 0 20 0",
		want: []geom.Point{{X: 0, Y: 0}, {X: 10, Y: 0}, {X: 20, Y: 0}},
	}, {
		name: "A degrades to a line to the endpoint",
		d:    "M 0 0 A 5 5 0 0 1 10 10",
		want: []geom.Point{{X: 0, Y: 0}, {X: 10, Y: 10}},
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePathD(tc.d)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d polylines, want 1", len(got))
			}
			assertSamePoints(t, got[0].Points, tc.want)
		})
	}
}

func TestParsePathDSplitsSubpaths(t *testing.T) {
	got, err := ParsePathD("M 0 0 L 10 0 M 20 0 L 30 0")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d polylines, want 2 (a moveto lifts the pen)", len(got))
	}
}

func TestParsePathDBezierEndsOnItsEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    string
		end  geom.Point
	}{
		{"cubic", "M 0 0 C 0 10 10 10 10 0", geom.Point{X: 10, Y: 0}},
		{"quadratic", "M 0 0 Q 5 10 10 0", geom.Point{X: 10, Y: 0}},
		{"smooth cubic", "M 0 0 C 0 10 10 10 10 0 S 20 -10 20 0", geom.Point{X: 20, Y: 0}},
		{"smooth quadratic", "M 0 0 Q 5 10 10 0 T 20 0", geom.Point{X: 20, Y: 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePathD(tc.d)
			if err != nil {
				t.Fatal(err)
			}
			pts := got[0].Points
			if last := pts[len(pts)-1]; !nearly(last, tc.end) {
				t.Errorf("curve ends at %+v, want %+v", last, tc.end)
			}
			// A flattened curve must actually bend, not shortcut.
			if len(pts) < 5 {
				t.Errorf("curve flattened to %d points, want a real polyline", len(pts))
			}
		})
	}
}

func TestParsePathDCurveIsSmooth(t *testing.T) {
	// A cubic arch: the midpoint must bulge away from the chord.
	got, err := ParsePathD("M 0 0 C 0 10 10 10 10 0")
	if err != nil {
		t.Fatal(err)
	}
	pts := got[0].Points
	mid := pts[len(pts)/2]
	if mid.Y <= 1 {
		t.Errorf("midpoint %+v did not follow the control points; the curve was flattened to a chord", mid)
	}
}

func TestParsePathDRejectsBadInput(t *testing.T) {
	tests := []struct{ name, d string }{
		{"empty", ""},
		{"starts with a number", "10 10 L 20 20"},
		{"truncated command", "M 0 0 L 10"},
		{"no coordinates", "M"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParsePathD(tc.d); err == nil {
				t.Errorf("ParsePathD(%q) = nil error, want a failure", tc.d)
			}
		})
	}
}

func TestExtractSVGPaths(t *testing.T) {
	svg, err := os.ReadFile(filepath.Join("testdata", "girl.svg"))
	if err != nil {
		t.Fatal(err)
	}
	ds := ExtractSVGPaths(string(svg))
	if len(ds) == 0 {
		t.Fatal("no paths extracted")
	}
	if _, err := ParsePaths(ds); err != nil {
		t.Errorf("paths from a real SVG failed to parse: %v", err)
	}
}

// TestGoldenSVG pins the flattened output of the drawings that were injected on
// the device in M0. It is a regression guard: these exact polylines are known
// to render correctly, so any change to the parser or the subdivision counts
// has to be a deliberate one. Refresh with `go test ./internal/layout -update`.
func TestGoldenSVG(t *testing.T) {
	for _, name := range []string{"girl", "cat"} {
		t.Run(name, func(t *testing.T) {
			svg, err := os.ReadFile(filepath.Join("testdata", name+".svg"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := ParsePaths(ExtractSVGPaths(string(svg)))
			if err != nil {
				t.Fatal(err)
			}
			// Compare in the injected form: fitted to a real page area.
			got = Fit(got, FitOptions{Area: geom.Rect{X: 100, Y: 120, W: 1204, H: 1632}, Scale: 1})

			golden := filepath.Join("testdata", name+".golden.json")
			if *update {
				b, err := json.MarshalIndent(round(got), "", " ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, append(b, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("wrote %s (%d strokes)", golden, len(got))
				return
			}

			b, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run: go test ./internal/layout -update)", err)
			}
			var want []geom.Stroke
			if err := json.Unmarshal(b, &want); err != nil {
				t.Fatal(err)
			}
			assertSameStrokes(t, round(got), want)
		})
	}
}

// round trims float noise so the golden files stay diffable and are not
// hostage to the last bits of the flattening arithmetic.
func round(ss []geom.Stroke) []geom.Stroke {
	out := make([]geom.Stroke, len(ss))
	for i, s := range ss {
		pts := make([]geom.Point, len(s.Points))
		for j, p := range s.Points {
			pts[j] = geom.Point{X: math.Round(p.X*100) / 100, Y: math.Round(p.Y*100) / 100}
		}
		out[i] = geom.Stroke{Points: pts}
	}
	return out
}

func nearly(a, b geom.Point) bool {
	return math.Abs(a.X-b.X) < 1e-6 && math.Abs(a.Y-b.Y) < 1e-6
}

func assertSamePoints(t *testing.T, got, want []geom.Point) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d points %+v, want %d %+v", len(got), got, len(want), want)
	}
	for i := range got {
		if !nearly(got[i], want[i]) {
			t.Errorf("point %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func assertSameStrokes(t *testing.T, got, want []geom.Stroke) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d strokes, want %d", len(got), len(want))
	}
	for i := range got {
		if len(got[i].Points) != len(want[i].Points) {
			t.Fatalf("stroke %d: got %d points, want %d", i, len(got[i].Points), len(want[i].Points))
		}
		for j := range got[i].Points {
			if !nearly(got[i].Points[j], want[i].Points[j]) {
				t.Fatalf("stroke %d point %d = %+v, want %+v", i, j, got[i].Points[j], want[i].Points[j])
			}
		}
	}
}
