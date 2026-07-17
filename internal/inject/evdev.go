package inject

import (
	"encoding/binary"
	"fmt"
	"io"
)

// evdev event types and codes (linux/input-event-codes.h).
const (
	evSyn = 0x00
	evKey = 0x01
	evAbs = 0x03

	synReport = 0x00

	btnToolPen    = 0x140
	btnToolFinger = 0x145
	btnTouch      = 0x14a

	absX            = 0x00
	absY            = 0x01
	absPressure     = 0x18
	absDistance     = 0x19
	absMtSlot       = 0x2f
	absMtPositionX  = 0x35
	absMtPositionY  = 0x36
	absMtTrackingID = 0x39
)

// eventSize is sizeof(struct input_event) on 32-bit ARM:
// timeval{int32 sec, int32 usec} + u16 type + u16 code + s32 value.
const eventSize = 16

// writer serialises input_events onto a device. The timestamp stays zero: the
// kernel stamps injected events itself.
type writer struct {
	w   io.Writer
	err error // first write error; subsequent calls are no-ops
}

func (w *writer) emit(typ, code uint16, val int32) {
	if w.err != nil {
		return
	}
	var b [eventSize]byte
	binary.LittleEndian.PutUint16(b[8:10], typ)
	binary.LittleEndian.PutUint16(b[10:12], code)
	binary.LittleEndian.PutUint32(b[12:16], uint32(val))
	if _, err := w.w.Write(b[:]); err != nil {
		w.err = fmt.Errorf("write evdev event type=%#x code=%#x: %w", typ, code, err)
	}
}

// report ends an event packet; the driver only acts on complete packets.
func (w *writer) report() { w.emit(evSyn, synReport, 0) }
