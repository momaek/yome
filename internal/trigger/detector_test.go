package trigger

import (
	"testing"
	"time"

	"github.com/momaek/yome/internal/geom"
)

// testOptions mirrors the config defaults (M0 calibration).
func testOptions() Options {
	return Options{
		Zone:      geom.Rect{X: 1104, Y: 1572, W: 300, H: 300},
		TapMax:    300 * time.Millisecond,
		GapMax:    600 * time.Millisecond,
		DistMax:   120,
		LongPress: 1500 * time.Millisecond,
	}
}

var (
	t0     = time.Unix(1000, 0)
	inZone = geom.Point{X: 1250, Y: 1700}
)

// at is shorthand for a moment n milliseconds after t0.
func at(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }

// tap feeds one press-release pair and returns the last result.
func tap(d *Detector, downMs, upMs int, p geom.Point) (Gesture, bool) {
	d.Frame(at(downMs), 1, p)
	return d.Frame(at(upMs), 0, p)
}

func TestDoubleTapFiresNewPage(t *testing.T) {
	d := NewDetector(testOptions())
	if _, ok := tap(d, 0, 100, inZone); ok {
		t.Fatal("single tap must not fire")
	}
	g, ok := tap(d, 300, 400, inZone)
	if !ok {
		t.Fatal("double tap did not fire")
	}
	if g.Mode != NewPage {
		t.Fatalf("mode = %v, want NewPage", g.Mode)
	}
	if g.At != inZone {
		t.Fatalf("At = %v, want %v", g.At, inZone)
	}
}

func TestLongPressSecondTapFiresInPlace(t *testing.T) {
	d := NewDetector(testOptions())
	tap(d, 0, 100, inZone)
	d.Frame(at(300), 1, inZone) // second press down...
	// ...held: no release. The hold crosses the threshold on a tick.
	if _, ok := d.Tick(at(1000)); ok {
		t.Fatal("fired before the long-press threshold")
	}
	g, ok := d.Tick(at(1900))
	if !ok {
		t.Fatal("long press did not fire")
	}
	if g.Mode != InPlace {
		t.Fatalf("mode = %v, want InPlace", g.Mode)
	}
	// The release afterwards must not fire anything more.
	if _, ok := d.Frame(at(2000), 0, inZone); ok {
		t.Fatal("release after in-place fired again")
	}
}

func TestLongPressFiresOnFrameToo(t *testing.T) {
	// A finger that wobbles slightly produces frames, not ticks; the
	// threshold must fire either way.
	d := NewDetector(testOptions())
	tap(d, 0, 100, inZone)
	d.Frame(at(300), 1, inZone)
	moved := geom.Point{X: inZone.X + 5, Y: inZone.Y}
	g, ok := d.Frame(at(1900), 1, moved)
	if !ok || g.Mode != InPlace {
		t.Fatalf("got (%v, %v), want InPlace", g, ok)
	}
}

func TestMediumHoldReleaseFiresNothing(t *testing.T) {
	// Released after TapMax but before LongPress: neither gesture.
	d := NewDetector(testOptions())
	tap(d, 0, 100, inZone)
	d.Frame(at(300), 1, inZone)
	if _, ok := d.Frame(at(1000), 0, inZone); ok {
		t.Fatal("medium hold fired a gesture")
	}
}

func TestRejects(t *testing.T) {
	out := geom.Point{X: 700, Y: 900}
	far := geom.Point{X: 1250, Y: 1572 + 250} // in zone but >DistMax from inZone

	cases := []struct {
		name string
		run  func(d *Detector) (Gesture, bool)
	}{
		{"slow first tap", func(d *Detector) (Gesture, bool) {
			tap(d, 0, 400, inZone) // 400ms press > TapMax
			return tap(d, 500, 600, inZone)
		}},
		{"gap too long", func(d *Detector) (Gesture, bool) {
			tap(d, 0, 100, inZone)
			return tap(d, 800, 900, inZone) // 700ms after release > GapMax
		}},
		{"taps too far apart", func(d *Detector) (Gesture, bool) {
			tap(d, 0, 100, inZone)
			d.Frame(at(300), 1, far)
			// far restarts as a fresh first tap; its release completes nothing.
			return d.Frame(at(400), 0, far)
		}},
		{"outside the zone", func(d *Detector) (Gesture, bool) {
			tap(d, 0, 100, out)
			return tap(d, 300, 400, out)
		}},
		{"drag during a tap", func(d *Detector) (Gesture, bool) {
			d.Frame(at(0), 1, inZone)
			d.Frame(at(50), 1, geom.Point{X: inZone.X - 200, Y: inZone.Y})
			d.Frame(at(100), 0, inZone)
			return tap(d, 300, 400, inZone)
		}},
		{"second finger cancels", func(d *Detector) (Gesture, bool) {
			tap(d, 0, 100, inZone)
			d.Frame(at(300), 1, inZone)
			d.Frame(at(350), 2, inZone) // second finger lands
			d.Frame(at(400), 0, inZone)
			return d.Frame(at(450), 0, inZone)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDetector(testOptions())
			if g, ok := tc.run(d); ok {
				t.Fatalf("fired %v, want nothing", g)
			}
		})
	}
}

func TestRestartAfterMissedWindow(t *testing.T) {
	// A late second tap restarts the sequence: it becomes tap one of a new
	// attempt, so a third tap right after it completes a double tap.
	d := NewDetector(testOptions())
	tap(d, 0, 100, inZone)
	tap(d, 800, 900, inZone) // too late for a double; becomes a fresh first tap
	g, ok := tap(d, 1100, 1200, inZone)
	if !ok || g.Mode != NewPage {
		t.Fatalf("got (%v, %v), want NewPage", g, ok)
	}
}

func TestResetDropsState(t *testing.T) {
	d := NewDetector(testOptions())
	tap(d, 0, 100, inZone)
	d.Reset()
	if _, ok := tap(d, 300, 400, inZone); ok {
		t.Fatal("tap after Reset completed a stale double tap")
	}
}

func TestTickExpiresDoubleTapWindow(t *testing.T) {
	d := NewDetector(testOptions())
	tap(d, 0, 100, inZone)
	d.Tick(at(800)) // window expires quietly
	if _, ok := tap(d, 900, 1000, inZone); ok {
		t.Fatal("expired window still completed a double tap")
	}
}

func TestOutsideZoneDragThroughZoneIgnored(t *testing.T) {
	// A drag that starts outside and passes through the zone must not arm
	// anything: the contact is disqualified until every finger lifts.
	d := NewDetector(testOptions())
	d.Frame(at(0), 1, geom.Point{X: 700, Y: 900})
	d.Frame(at(50), 1, inZone)
	d.Frame(at(100), 0, inZone)
	if _, ok := tap(d, 200, 300, inZone); ok {
		t.Fatal("single tap after a drag-through fired")
	}
}
