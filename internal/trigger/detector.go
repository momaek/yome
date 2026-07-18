// Package trigger recognises the corner gestures that start an AI session:
// a double tap in the trigger zone opens a new-page session, and a double tap
// whose second press is held turns into the in-place session (the hold itself
// is the confirmation for the destructive mode — plan 4.1).
//
// The detector is a pure state machine over settled touch frames, so every
// timing rule is unit-testable off-device. Reading the touchscreen and feeding
// frames in lives in listener.go.
//
// The package also captures pen-eraser strokes from the Wacom digitizer
// (eraser.go) — not a session trigger, but the same read-only evdev idiom.
package trigger

import (
	"math"
	"time"

	"github.com/momaek/yome/internal/geom"
)

// Mode is what the gesture asks for.
type Mode int

const (
	// NewPage is the default session: results go to a fresh page, the user's
	// ink is never touched.
	NewPage Mode = iota
	// InPlace is the destructive session: the page may be erased and
	// rewritten. Only reachable through the long-press variant.
	InPlace
)

func (m Mode) String() string {
	if m == InPlace {
		return "in-place"
	}
	return "new-page"
}

// Gesture is one recognised trigger.
type Gesture struct {
	Mode Mode
	At   geom.Point // where the second tap landed, physical screen space
}

// Options carries the calibrated gesture thresholds (config [gesture]).
type Options struct {
	// Zone is the trigger area in physical screen coordinates. The physical
	// bottom-right corner is inert to xochitl in portrait (M0); note that a
	// landscape notebook shows this corner at the view's bottom-left.
	Zone geom.Rect

	TapMax    time.Duration // a press longer than this is not a tap
	GapMax    time.Duration // max release-to-press delay between the taps
	DistMax   float64       // max travel within and between the taps, px
	LongPress time.Duration // second press held this long = in-place mode
}

// state is where the machine is between frames.
type state int

const (
	// stIdle: no touch history worth keeping.
	stIdle state = iota
	// stFirstDown: a finger is down inside the zone, might become tap one.
	stFirstDown
	// stAwaitSecond: one clean tap done, waiting for the second press.
	stAwaitSecond
	// stSecondDown: the second press is down; release fast = NewPage,
	// hold = InPlace.
	stSecondDown
	// stIgnore: the current contact is disqualified; wait for all fingers
	// to lift before considering anything again.
	stIgnore
)

// Detector recognises gestures from a stream of touch frames.
type Detector struct {
	o  Options
	st state

	downT time.Time  // when the current press started
	downP geom.Point // where the current press started
	upT   time.Time  // when the first tap was released

	touching bool // contact state after the last frame
}

// NewDetector builds a detector with the given thresholds.
func NewDetector(o Options) *Detector { return &Detector{o: o} }

// Reset drops any half-recognised gesture, e.g. when the daemon resumes
// listening after a session and must not act on stale state.
func (d *Detector) Reset() {
	d.st = stIdle
	d.touching = false
}

// Frame consumes one settled touch frame: how many fingers are down at time t,
// and where slot 0 is. It reports a gesture the moment one completes.
func (d *Detector) Frame(t time.Time, fingers int, p geom.Point) (Gesture, bool) {
	if fingers > 1 {
		// A second finger disqualifies whatever was building up.
		d.st = stIgnore
		d.touching = true
		return Gesture{}, false
	}
	touching := fingers == 1
	defer func() { d.touching = touching }()

	switch d.st {
	case stIgnore:
		if !touching {
			d.st = stIdle
		}

	case stIdle:
		if !touching {
			break
		}
		if d.o.Zone.Contains(p) {
			d.st, d.downT, d.downP = stFirstDown, t, p
		} else {
			d.st = stIgnore
		}

	case stFirstDown:
		if touching {
			if dist(p, d.downP) > d.o.DistMax {
				d.st = stIgnore
			}
			break
		}
		if t.Sub(d.downT) <= d.o.TapMax {
			d.st, d.upT = stAwaitSecond, t
		} else {
			d.st = stIdle
		}

	case stAwaitSecond:
		if !touching {
			if t.Sub(d.upT) > d.o.GapMax {
				d.st = stIdle
			}
			break
		}
		switch {
		case t.Sub(d.upT) <= d.o.GapMax && d.o.Zone.Contains(p) && dist(p, d.downP) <= d.o.DistMax:
			d.st, d.downT, d.downP = stSecondDown, t, p
		case d.o.Zone.Contains(p):
			// Too late or too far apart for a double tap, but a fresh press in
			// the zone can start a new attempt.
			d.st, d.downT, d.downP = stFirstDown, t, p
		default:
			d.st = stIgnore
		}

	case stSecondDown:
		if touching {
			if dist(p, d.downP) > d.o.DistMax {
				d.st = stIgnore
				break
			}
			if t.Sub(d.downT) >= d.o.LongPress {
				// Fire on the threshold, not on release: the hold is the
				// confirmation, and the user gets feedback while still pressing.
				d.st = stIgnore
				return Gesture{Mode: InPlace, At: d.downP}, true
			}
			break
		}
		held := t.Sub(d.downT)
		d.st = stIdle
		if held <= d.o.TapMax {
			return Gesture{Mode: NewPage, At: d.downP}, true
		}
		// Released between TapMax and LongPress: neither gesture; drop it.
	}
	return Gesture{}, false
}

// Tick advances time with no input change. The listener calls it periodically
// because a finger held perfectly still produces no events, yet the long-press
// must fire while it is down.
func (d *Detector) Tick(t time.Time) (Gesture, bool) {
	switch d.st {
	case stAwaitSecond:
		if t.Sub(d.upT) > d.o.GapMax {
			d.st = stIdle
		}
	case stSecondDown:
		if d.touching && t.Sub(d.downT) >= d.o.LongPress {
			d.st = stIgnore
			return Gesture{Mode: InPlace, At: d.downP}, true
		}
	}
	return Gesture{}, false
}

func dist(a, b geom.Point) float64 {
	return math.Hypot(a.X-b.X, a.Y-b.Y)
}
