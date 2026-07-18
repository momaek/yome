package inject

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/momaek/yome/internal/geom"
)

// event is a decoded input_event, for asserting on what we put on the wire.
type event struct {
	Type, Code uint16
	Value      int32
}

func decode(t *testing.T, b []byte) []event {
	t.Helper()
	if len(b)%eventSize != 0 {
		t.Fatalf("stream is %d bytes, not a whole number of %d-byte events", len(b), eventSize)
	}
	var out []event
	for o := 0; o+eventSize <= len(b); o += eventSize {
		// The timestamp must stay zero: the kernel stamps injected events.
		if sec := binary.LittleEndian.Uint32(b[o:]); sec != 0 {
			t.Errorf("event at %d carries a timestamp %d, want 0", o, sec)
		}
		out = append(out, event{
			Type:  binary.LittleEndian.Uint16(b[o+8:]),
			Code:  binary.LittleEndian.Uint16(b[o+10:]),
			Value: int32(binary.LittleEndian.Uint32(b[o+12:])),
		})
	}
	return out
}

func find(evs []event, typ, code uint16) []int32 {
	var out []int32
	for _, e := range evs {
		if e.Type == typ && e.Code == code {
			out = append(out, e.Value)
		}
	}
	return out
}

func testInjector(t *testing.T) (*Injector, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	pen, touch := &bytes.Buffer{}, &bytes.Buffer{}
	return newForTest(pen, touch, DefaultOptions()), pen, touch
}

func TestStrokesEventSequence(t *testing.T) {
	in, pen, _ := testInjector(t)
	line := geom.Stroke{Points: []geom.Point{{X: 100, Y: 100}, {X: 200, Y: 100}}}
	if err := in.Strokes([]geom.Stroke{line}); err != nil {
		t.Fatal(err)
	}
	evs := decode(t, pen.Bytes())

	// The tool must be announced before the stroke and retracted after it.
	tool := find(evs, evKey, btnToolPen)
	if len(tool) != 2 || tool[0] != 1 || tool[1] != 0 {
		t.Errorf("BTN_TOOL_PEN sequence = %v, want [1 0]", tool)
	}
	touch := find(evs, evKey, btnTouch)
	if len(touch) != 2 || touch[0] != 1 || touch[1] != 0 {
		t.Errorf("BTN_TOUCH sequence = %v, want [1 0]", touch)
	}

	// The last event of the packet must be a SYN_REPORT, or the driver never
	// acts on it.
	if last := evs[len(evs)-1]; last.Type != evSyn || last.Code != synReport {
		t.Errorf("stream ends with %+v, want a SYN_REPORT", last)
	}
	// Pressure must return to zero before the pen lifts.
	if p := find(evs, evAbs, absPressure); p[len(p)-1] != 0 {
		t.Errorf("final pressure = %d, want 0", p[len(p)-1])
	}
}

func TestEraseAnnouncesRubberNotPen(t *testing.T) {
	in, pen, _ := testInjector(t)
	line := geom.Stroke{Points: []geom.Point{{X: 100, Y: 100}, {X: 200, Y: 100}}}
	if err := in.Erase([]geom.Stroke{line}); err != nil {
		t.Fatal(err)
	}
	evs := decode(t, pen.Bytes())

	rubber := find(evs, evKey, btnToolRubber)
	if len(rubber) != 2 || rubber[0] != 1 || rubber[1] != 0 {
		t.Errorf("BTN_TOOL_RUBBER sequence = %v, want [1 0]", rubber)
	}
	// Announcing the pen too would leave xochitl guessing which tool this is.
	if p := find(evs, evKey, btnToolPen); len(p) != 0 {
		t.Errorf("BTN_TOOL_PEN events = %v, want none", p)
	}
	touch := find(evs, evKey, btnTouch)
	if len(touch) != 2 || touch[0] != 1 || touch[1] != 0 {
		t.Errorf("BTN_TOUCH sequence = %v, want [1 0]", touch)
	}
}

func TestStrokesLandsBeforeTouchingDown(t *testing.T) {
	in, pen, _ := testInjector(t)
	if err := in.Strokes([]geom.Stroke{{Points: []geom.Point{{X: 100, Y: 100}, {X: 200, Y: 100}}}}); err != nil {
		t.Fatal(err)
	}
	evs := decode(t, pen.Bytes())

	// The pen must be positioned before BTN_TOUCH: touching down at a stale
	// position drags a line in from wherever the pen last was.
	var sawX, sawY bool
	for _, e := range evs {
		if e.Type == evAbs && e.Code == absX {
			sawX = true
		}
		if e.Type == evAbs && e.Code == absY {
			sawY = true
		}
		if e.Type == evKey && e.Code == btnTouch && e.Value == 1 {
			if !sawX || !sawY {
				t.Error("BTN_TOUCH=1 arrived before the pen was positioned")
			}
			return
		}
	}
	t.Error("no BTN_TOUCH=1 in the stream")
}

func TestStrokesInterpolates(t *testing.T) {
	opts := DefaultOptions()
	opts.PointSpacing = 10
	in := newForTest(&bytes.Buffer{}, &bytes.Buffer{}, opts)
	pen := &bytes.Buffer{}
	in.pen = pen

	// A 100px line at 10px spacing needs ~10 intermediate points; xochitl
	// draws straight lines between points, so sparse points show as chords.
	if err := in.Strokes([]geom.Stroke{{Points: []geom.Point{{X: 100, Y: 100}, {X: 200, Y: 100}}}}); err != nil {
		t.Fatal(err)
	}
	xs := find(decode(t, pen.Bytes()), evAbs, absX)
	if len(xs) < 10 {
		t.Errorf("a 100px line at 10px spacing emitted %d positions, want ~11", len(xs))
	}
}

func TestStrokesSkipsDegenerateStrokes(t *testing.T) {
	in, pen, _ := testInjector(t)
	// A single point cannot make a mark; it must not emit a touch-down.
	err := in.Strokes([]geom.Stroke{{Points: []geom.Point{{X: 10, Y: 10}}}, {Points: nil}})
	if err != nil {
		t.Fatal(err)
	}
	if got := find(decode(t, pen.Bytes()), evKey, btnTouch); len(got) != 0 {
		t.Errorf("BTN_TOUCH emitted %v for degenerate strokes, want none", got)
	}
}

func TestStrokesReportsWriteFailures(t *testing.T) {
	in := newForTest(errWriter{}, &bytes.Buffer{}, DefaultOptions())
	err := in.Strokes([]geom.Stroke{{Points: []geom.Point{{X: 0, Y: 0}, {X: 10, Y: 10}}}})
	if err == nil {
		t.Fatal("a failing device wrote no error")
	}
	if !errors.Is(err, errBroken) {
		t.Errorf("error %v does not wrap the device's error", err)
	}
}

type errWriter struct{}

var errBroken = errors.New("device is gone")

func (errWriter) Write([]byte) (int, error) { return 0, errBroken }

func TestPressureFollowsTheEnvelope(t *testing.T) {
	in, pen, _ := testInjector(t)
	// A long stroke, so the envelope applies rather than the short-stroke
	// constant-pressure path.
	if err := in.Strokes([]geom.Stroke{{Points: []geom.Point{{X: 100, Y: 100}, {X: 900, Y: 100}}}}); err != nil {
		t.Fatal(err)
	}
	p := find(decode(t, pen.Bytes()), evAbs, absPressure)
	p = p[:len(p)-1] // drop the trailing zero that lifts the pen

	base := int32(DefaultPressure().Base)
	if p[0] >= base {
		t.Errorf("stroke starts at full pressure %d; the attack ramp is missing", p[0])
	}
	if mid := p[len(p)/2]; mid != base {
		t.Errorf("mid-stroke pressure = %d, want the full %d", mid, base)
	}
	if end := p[len(p)-1]; end >= base {
		t.Errorf("stroke ends at %d, want a taper below %d", end, base)
	}
}

func TestTapHoldsThenReleases(t *testing.T) {
	in, _, touch := testInjector(t)
	if err := in.Tap(geom.Point{X: 55, Y: 52}, 0); err != nil {
		t.Fatal(err)
	}
	evs := decode(t, touch.Bytes())

	// A tap is a tracking id appearing and then going away.
	ids := find(evs, evAbs, absMtTrackingID)
	if len(ids) != 2 || ids[0] < 0 || ids[1] != -1 {
		t.Errorf("tracking id sequence = %v, want a positive id then -1", ids)
	}
	// The tap point goes through the touchscreen's flipped y axis.
	wantX, wantY := ToTouch(geom.Point{X: 55, Y: 52})
	if got := find(evs, evAbs, absMtPositionX); len(got) != 1 || got[0] != wantX {
		t.Errorf("touch x = %v, want [%d]", got, wantX)
	}
	if got := find(evs, evAbs, absMtPositionY); len(got) != 1 || got[0] != wantY {
		t.Errorf("touch y = %v, want [%d]", got, wantY)
	}
}

func TestSwipeMovesAcrossTheScreen(t *testing.T) {
	in, _, touch := testInjector(t)
	from, to := geom.Point{X: 1200, Y: 900}, geom.Point{X: 200, Y: 900}
	if err := in.Swipe(from, to, 20, 0); err != nil {
		t.Fatal(err)
	}
	evs := decode(t, touch.Bytes())

	xs := find(evs, evAbs, absMtPositionX)
	if len(xs) < 20 {
		t.Fatalf("swipe emitted %d positions, want at least 20", len(xs))
	}
	firstX, _ := ToTouch(from)
	lastX, _ := ToTouch(to)
	if xs[0] != firstX {
		t.Errorf("swipe starts at x=%d, want %d", xs[0], firstX)
	}
	if xs[len(xs)-1] != lastX {
		t.Errorf("swipe ends at x=%d, want %d", xs[len(xs)-1], lastX)
	}
	if ids := find(evs, evAbs, absMtTrackingID); ids[len(ids)-1] != -1 {
		t.Error("swipe never released the contact")
	}
}
