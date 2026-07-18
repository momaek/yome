package inject

import (
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/momaek/yome/internal/geom"
)

// Device names on firmware 3.11.2.5. Enumerated by name, never by event number.
const (
	PenDeviceName   = "Wacom I2C Digitizer"
	TouchDeviceName = "pt_mt"
)

// Options configures injection timing and feel.
type Options struct {
	PenDevice   string // evdev name of the digitizer
	TouchDevice string // evdev name of the touchscreen

	// PointDelay is the pause between points. Too fast and xochitl drops
	// points, breaking strokes; too slow and a page takes minutes. 4-5ms was
	// clean on device (M0).
	PointDelay time.Duration
	// PointSpacing is the interpolation step along a segment, in pixels.
	PointSpacing float64
	// LandingPause is how long the pen hovers over the start point before
	// touching down, giving xochitl time to register the position.
	LandingPause time.Duration

	Pressure PressureEnvelope
}

// DefaultOptions returns the M0-calibrated settings.
func DefaultOptions() Options {
	return Options{
		PenDevice:    PenDeviceName,
		TouchDevice:  TouchDeviceName,
		PointDelay:   5 * time.Millisecond,
		PointSpacing: 3,
		LandingPause: 15 * time.Millisecond,
		Pressure:     DefaultPressure(),
	}
}

// Injector writes synthetic events to the pen and touch devices.
type Injector struct {
	pen     io.Writer
	touch   io.Writer
	closers []io.Closer
	opts    Options
	sleep   func(time.Duration)
}

// Open finds the pen and touch devices by name and opens them for writing.
// Devices are opened lazily-tolerant: a missing touchscreen is only an error
// once a touch action is attempted.
func Open(opts Options) (*Injector, error) {
	in := &Injector{opts: opts, sleep: time.Sleep}

	penPath, err := FindDevice(opts.PenDevice)
	if err != nil {
		return nil, fmt.Errorf("locate pen device: %w", err)
	}
	pen, err := os.OpenFile(penPath, os.O_WRONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("open pen device %s: %w", penPath, err)
	}
	in.pen, in.closers = pen, append(in.closers, pen)

	touchPath, err := FindDevice(opts.TouchDevice)
	if err != nil {
		in.Close()
		return nil, fmt.Errorf("locate touch device: %w", err)
	}
	touch, err := os.OpenFile(touchPath, os.O_WRONLY, 0)
	if err != nil {
		in.Close()
		return nil, fmt.Errorf("open touch device %s: %w", touchPath, err)
	}
	in.touch, in.closers = touch, append(in.closers, touch)

	return in, nil
}

// newForTest builds an Injector over in-memory sinks with no real sleeping.
func newForTest(pen, touch io.Writer, opts Options) *Injector {
	return &Injector{pen: pen, touch: touch, opts: opts, sleep: func(time.Duration) {}}
}

// Close releases the device handles.
func (in *Injector) Close() error {
	var first error
	for _, c := range in.closers {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	in.closers = nil
	return first
}

// Strokes injects a set of pen strokes. It is synchronous: it returns once the
// last point has been written, which for a full page of text is tens of
// seconds.
//
// Strokes are drawn with whatever tool xochitl currently has selected — if
// that is the eraser, this silently erases instead of writing. Callers must
// force the pen tool first (see internal/ui) and verify ink afterwards.
func (in *Injector) Strokes(strokes []geom.Stroke) error {
	return in.trace(btnToolPen, strokes)
}

// Erase traces the same trajectories with the rubber tool announced instead
// of the pen — the synthetic twin of the Marker Plus eraser end. xochitl maps
// BTN_TOOL_RUBBER to the eraser regardless of which tool the toolbar has
// selected (that is why the hardware eraser needs no tool switch), so unlike
// Strokes this needs no UI preparation and cannot be hijacked by the user's
// tool selection. The erased band's width follows xochitl's eraser size
// setting.
func (in *Injector) Erase(strokes []geom.Stroke) error {
	return in.trace(btnToolRubber, strokes)
}

// hoverDistance is the ABS_DISTANCE the synthetic rubber "approaches" from
// during its proximity-in ramp.
const hoverDistance = 60

// firstDrawable returns the first stroke that would actually be traced, or
// nil when every stroke is degenerate.
func firstDrawable(strokes []geom.Stroke) *geom.Stroke {
	for i := range strokes {
		if len(strokes[i].Points) >= 2 {
			return &strokes[i]
		}
	}
	return nil
}

// trace injects a set of polylines under the given tool announcement.
func (in *Injector) trace(tool uint16, strokes []geom.Stroke) error {
	w := &writer{w: in.pen}

	// The hardware announces a tool with its position and hover distance in
	// the same packet, then closes the distance to zero before touching down.
	// The bare announcement that satisfies xochitl for the pen was ignored
	// for the rubber (on-device, fw 3.27.3.0), so mimic the real
	// proximity-in for it.
	w.emit(evKey, tool, 1)
	if first := firstDrawable(strokes); tool == btnToolRubber && first != nil {
		x, y := ToWacom(first.Points[0])
		w.emit(evAbs, absX, x)
		w.emit(evAbs, absY, y)
		w.emit(evAbs, absDistance, hoverDistance)
		w.report()
		for d := int32(hoverDistance) - 20; d >= 0; d -= 20 {
			w.emit(evAbs, absDistance, d)
			w.report()
			in.sleep(in.opts.PointDelay)
		}
	} else {
		w.report()
	}
	in.sleep(50 * time.Millisecond)

	for i, s := range strokes {
		if len(s.Points) < 2 {
			continue // a lone point makes no mark
		}
		in.stroke(w, s)
		if w.err != nil {
			return fmt.Errorf("stroke %d/%d: %w", i+1, len(strokes), w.err)
		}
	}

	w.emit(evKey, tool, 0)
	w.report()
	if w.err != nil {
		return fmt.Errorf("lift tool: %w", w.err)
	}
	return nil
}

// stroke draws one polyline: hover to the start, touch down, trace with the
// pressure envelope, lift.
func (in *Injector) stroke(w *writer, s geom.Stroke) {
	total := s.Length()
	press := func(frac float64) int32 { return in.opts.Pressure.At(frac, total) }

	x, y := ToWacom(s.Points[0])
	w.emit(evAbs, absX, x)
	w.emit(evAbs, absY, y)
	w.emit(evAbs, absDistance, 0)
	w.report()
	in.sleep(in.opts.LandingPause)

	w.emit(evKey, btnTouch, 1)
	w.emit(evAbs, absPressure, press(0))
	w.report()

	var traveled float64
	for i := 1; i < len(s.Points); i++ {
		a, b := s.Points[i-1], s.Points[i]
		dx, dy := b.X-a.X, b.Y-a.Y
		seg := math.Hypot(dx, dy)

		// Interpolate: xochitl joins consecutive points with straight lines,
		// so long jumps would show as chords on curves.
		n := 1
		if in.opts.PointSpacing > 0 {
			n = int(seg/in.opts.PointSpacing) + 1
		}
		for j := 1; j <= n; j++ {
			t := float64(j) / float64(n)
			x, y := ToWacom(geom.Point{X: a.X + dx*t, Y: a.Y + dy*t})
			frac := 0.0
			if total > 0 {
				frac = (traveled + seg*t) / total
			}
			w.emit(evAbs, absX, x)
			w.emit(evAbs, absY, y)
			w.emit(evAbs, absPressure, press(frac))
			w.report()
			in.sleep(in.opts.PointDelay)
		}
		traveled += seg
	}

	w.emit(evAbs, absPressure, 0)
	w.emit(evKey, btnTouch, 0)
	w.report()
	in.sleep(in.opts.LandingPause)
}

// Tap presses the touchscreen at a screen-space point for the given duration.
// Buttons need >=60ms; opening a tool's options panel needs a ~200ms hold.
func (in *Injector) Tap(p geom.Point, hold time.Duration) error {
	w := &writer{w: in.touch}
	x, y := ToTouch(p)

	w.emit(evAbs, absMtSlot, 0)
	w.emit(evAbs, absMtTrackingID, 1)
	w.emit(evAbs, absMtPositionX, x)
	w.emit(evAbs, absMtPositionY, y)
	w.emit(evKey, btnTouch, 1)
	w.emit(evKey, btnToolFinger, 1)
	w.report()

	in.sleep(hold)

	w.emit(evAbs, absMtTrackingID, -1)
	w.emit(evKey, btnTouch, 0)
	w.emit(evKey, btnToolFinger, 0)
	w.report()

	if w.err != nil {
		return fmt.Errorf("tap at (%.0f,%.0f): %w", p.X, p.Y, w.err)
	}
	return nil
}

// Swipe drags one finger across the screen. A left swipe turns to the next
// page, a right swipe to the previous one.
func (in *Injector) Swipe(from, to geom.Point, steps int, delay time.Duration) error {
	if steps < 1 {
		steps = 1
	}
	w := &writer{w: in.touch}

	x, y := ToTouch(from)
	w.emit(evAbs, absMtSlot, 0)
	w.emit(evAbs, absMtTrackingID, 1)
	w.emit(evAbs, absMtPositionX, x)
	w.emit(evAbs, absMtPositionY, y)
	w.emit(evKey, btnTouch, 1)
	w.emit(evKey, btnToolFinger, 1)
	w.report()

	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		x, y := ToTouch(geom.Point{
			X: from.X + (to.X-from.X)*t,
			Y: from.Y + (to.Y-from.Y)*t,
		})
		w.emit(evAbs, absMtPositionX, x)
		w.emit(evAbs, absMtPositionY, y)
		w.report()
		in.sleep(delay)
	}

	w.emit(evAbs, absMtTrackingID, -1)
	w.emit(evKey, btnTouch, 0)
	w.emit(evKey, btnToolFinger, 0)
	w.report()

	if w.err != nil {
		return fmt.Errorf("swipe (%.0f,%.0f)->(%.0f,%.0f): %w", from.X, from.Y, to.X, to.Y, w.err)
	}
	return nil
}
