package inject

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"
)

// Event is one decoded evdev event.
type Event struct {
	Type  uint16
	Code  uint16
	Value int32
}

// String renders the event with symbolic names for the codes this project
// works with, matching the m0 record tool's output.
func (e Event) String() string {
	typ := fmt.Sprintf("T%d", e.Type)
	switch e.Type {
	case evSyn:
		typ = "SYN"
	case evKey:
		typ = "KEY"
	case evAbs:
		typ = "ABS"
	}
	code := fmt.Sprintf("0x%02x", e.Code)
	if e.Type == evKey {
		switch e.Code {
		case btnToolPen:
			code = "BTN_TOOL_PEN"
		case btnTouch:
			code = "BTN_TOUCH"
		case btnToolFinger:
			code = "BTN_TOOL_FINGER"
		}
	}
	if e.Type == evAbs {
		if n, ok := absNames[e.Code]; ok {
			code = n
		}
	}
	return fmt.Sprintf("%s %-20s %d", typ, code, e.Value)
}

var absNames = map[uint16]string{
	absX: "ABS_X", absY: "ABS_Y",
	absPressure: "ABS_PRESSURE", absDistance: "ABS_DISTANCE",
	0x1a: "ABS_TILT_X", 0x1b: "ABS_TILT_Y",
	absMtSlot: "ABS_MT_SLOT", 0x30: "ABS_MT_TOUCH_MAJOR", 0x31: "ABS_MT_TOUCH_MINOR",
	0x34: "ABS_MT_ORIENTATION", absMtPositionX: "ABS_MT_POSITION_X", absMtPositionY: "ABS_MT_POSITION_Y",
	0x37: "ABS_MT_TOOL_TYPE", absMtTrackingID: "ABS_MT_TRACKING_ID", 0x3a: "ABS_MT_PRESSURE",
}

// Record reads raw events from an input device for the given duration and
// hands each to fn. This is the calibration tool behind recording real
// interactions (e.g. the M3 add-page experiment): dump what a human's gesture
// produces, then replay the minimal sequence.
func Record(path string, d time.Duration, fn func(Event)) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s for recording: %w", path, err)
	}
	defer f.Close()

	deadline := time.Now().Add(d)
	if err := f.SetReadDeadline(deadline); err != nil {
		// Some kernels refuse deadlines on device files; fall back to a hard
		// stop via closing from a timer.
		t := time.AfterFunc(d, func() { f.Close() })
		defer t.Stop()
	}

	buf := make([]byte, eventSize*64)
	for time.Now().Before(deadline) {
		n, err := f.Read(buf)
		if err != nil {
			if os.IsTimeout(err) || err == io.EOF || err == os.ErrClosed {
				return nil
			}
			// Reads racing the deadline-close surface as "file already closed".
			if time.Now().After(deadline) {
				return nil
			}
			return fmt.Errorf("read %s: %w", path, err)
		}
		for o := 0; o+eventSize <= n; o += eventSize {
			fn(Event{
				Type:  binary.LittleEndian.Uint16(buf[o+8:]),
				Code:  binary.LittleEndian.Uint16(buf[o+10:]),
				Value: int32(binary.LittleEndian.Uint32(buf[o+12:])),
			})
		}
	}
	return nil
}
