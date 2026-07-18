package trigger

import (
	"math"
	"testing"

	"github.com/momaek/yome/internal/geom"
	"github.com/momaek/yome/internal/inject"
)

// scrub feeds a run of erasing frames along the given points, one frame per
// 10ms starting at startMs, and returns the result of the lift frame.
func scrub(d *EraserDetector, startMs int, pts ...geom.Point) (EraseStroke, bool) {
	ms := startMs
	for _, p := range pts {
		d.Frame(at(ms), true, p)
		ms += 10
	}
	return d.Frame(at(ms), false, pts[len(pts)-1])
}

func TestEraseStrokeBBoxAndTiming(t *testing.T) {
	d := &EraserDetector{}
	s, ok := scrub(d, 0,
		geom.Point{X: 200, Y: 500},
		geom.Point{X: 400, Y: 480},
		geom.Point{X: 300, Y: 620},
	)
	if !ok {
		t.Fatal("lift did not finish the stroke")
	}
	want := geom.Rect{X: 200, Y: 480, W: 200, H: 140}
	if s.BBox != want {
		t.Fatalf("BBox = %+v, want %+v", s.BBox, want)
	}
	if s.Points != 3 {
		t.Fatalf("Points = %d, want 3", s.Points)
	}
	if s.Start != at(0) || s.End != at(30) {
		t.Fatalf("Start/End = %v/%v, want %v/%v", s.Start, s.End, at(0), at(30))
	}
}

func TestEraserHoverEmitsNothing(t *testing.T) {
	d := &EraserDetector{}
	// Rubber in proximity but never touching: the listener feeds
	// erasing=false for hover frames, so nothing may accumulate.
	for ms := 0; ms < 100; ms += 10 {
		if _, ok := d.Frame(at(ms), false, geom.Point{X: 700, Y: 900}); ok {
			t.Fatal("hover frame finished a stroke")
		}
	}
}

func TestEraserSeparateContactsAreSeparateStrokes(t *testing.T) {
	d := &EraserDetector{}
	s1, ok := scrub(d, 0, geom.Point{X: 100, Y: 100}, geom.Point{X: 150, Y: 100})
	if !ok {
		t.Fatal("first stroke did not finish")
	}
	s2, ok := scrub(d, 500, geom.Point{X: 800, Y: 800})
	if !ok {
		t.Fatal("second stroke did not finish")
	}
	if s1.BBox == s2.BBox {
		t.Fatal("strokes were merged")
	}
	if s2.Start != at(500) {
		t.Fatalf("second stroke Start = %v, want %v", s2.Start, at(500))
	}
}

func TestEraserRepeatedLiftFramesEmitOnce(t *testing.T) {
	d := &EraserDetector{}
	d.Frame(at(0), true, geom.Point{X: 100, Y: 100})
	if _, ok := d.Frame(at(10), false, geom.Point{X: 100, Y: 100}); !ok {
		t.Fatal("lift did not finish the stroke")
	}
	if _, ok := d.Frame(at(20), false, geom.Point{X: 100, Y: 100}); ok {
		t.Fatal("idle frame after the lift emitted again")
	}
}

func TestEraserResetDropsActiveStroke(t *testing.T) {
	d := &EraserDetector{}
	d.Frame(at(0), true, geom.Point{X: 100, Y: 100})
	d.Reset()
	if _, ok := d.Frame(at(10), false, geom.Point{X: 100, Y: 100}); ok {
		t.Fatal("stroke survived Reset")
	}
}

// TestWacomToScreenInvertsToWacom pins the coordinate mapping against the
// injection side's calibrated forward transform.
func TestWacomToScreenInvertsToWacom(t *testing.T) {
	for _, p := range []geom.Point{
		{X: 0, Y: 0},
		{X: geom.ScreenW, Y: 0},
		{X: 0, Y: geom.ScreenH},
		{X: geom.ScreenW, Y: geom.ScreenH},
		{X: geom.ScreenW / 2, Y: geom.ScreenH / 2},
		{X: 137, Y: 1600},
	} {
		x, y := inject.ToWacom(p)
		got := wacomToScreen(x, y)
		// One digitizer step is well under a screen pixel; allow rounding.
		if math.Abs(got.X-p.X) > 1 || math.Abs(got.Y-p.Y) > 1 {
			t.Errorf("round trip %+v -> (%d,%d) -> %+v", p, x, y, got)
		}
	}
}
