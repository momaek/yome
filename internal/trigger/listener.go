package trigger

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/momaek/yome/internal/geom"
)

// Kernel input ABI (linux/input-event-codes.h). These are stable kernel
// constants, duplicated here so the detector side stays self-contained.
const (
	evSyn = 0x00
	evAbs = 0x03

	synReport = 0x00

	absMtSlot       = 0x2f
	absMtPositionX  = 0x35
	absMtPositionY  = 0x36
	absMtTrackingID = 0x39
)

// eventSize is sizeof(struct input_event) on 32-bit ARM.
const eventSize = 16

// touchMaxY mirrors the touchscreen's flipped y axis (see inject.ToTouch);
// reading events performs the inverse flip so the detector works in screen
// space like everything else.
const touchMaxY = 1871

// tickInterval is how often the long-press timer is checked when the finger
// rests still and the device goes quiet.
const tickInterval = 100 * time.Millisecond

// maxSlots is how many multitouch slots are tracked. The panel reports more,
// but past two fingers the gesture is disqualified anyway.
const maxSlots = 8

// Listener reads the touchscreen and emits recognised gestures.
//
// It only reads — the device is never grabbed, so xochitl keeps seeing every
// touch (plan 4.1). Pause suspends recognition while the daemon injects its
// own touch events, which arrive on this very device and must not be
// mistaken for the user.
type Listener struct {
	f      *os.File
	ch     chan Gesture
	paused atomic.Bool
	log    *slog.Logger

	mu  sync.Mutex // guards det
	det *Detector

	done chan struct{}
}

// Listen opens the touch device read-only and starts recognising.
func Listen(devPath string, o Options, log *slog.Logger) (*Listener, error) {
	if log == nil {
		log = slog.Default()
	}
	f, err := os.Open(devPath)
	if err != nil {
		return nil, fmt.Errorf("open touch device %s: %w", devPath, err)
	}
	l := &Listener{
		f:    f,
		ch:   make(chan Gesture, 1),
		log:  log,
		det:  NewDetector(o),
		done: make(chan struct{}),
	}
	go l.readLoop()
	go l.tickLoop()
	return l, nil
}

// Gestures is the stream of recognised triggers. When a gesture completes
// while a previous one is still unconsumed, the new one is dropped — sessions
// take minutes and queueing triggers behind one would fire them much later.
func (l *Listener) Gestures() <-chan Gesture { return l.ch }

// Pause suspends recognition and drops any half-built state.
func (l *Listener) Pause() {
	l.paused.Store(true)
	l.mu.Lock()
	l.det.Reset()
	l.mu.Unlock()
}

// Resume re-enables recognition from a clean slate.
func (l *Listener) Resume() {
	l.mu.Lock()
	l.det.Reset()
	l.mu.Unlock()
	l.paused.Store(false)
}

// Close stops both loops and releases the device.
func (l *Listener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return l.f.Close()
}

// emit hands a gesture to the channel unless paused, dropping when full.
func (l *Listener) emit(g Gesture, ok bool) {
	if !ok || l.paused.Load() {
		return
	}
	select {
	case l.ch <- g:
	default:
		l.log.Warn("gesture dropped: previous trigger not yet consumed", "mode", g.Mode.String())
	}
}

// tickLoop drives the time-based transitions (long-press, double-tap window)
// while the device is quiet.
func (l *Listener) tickLoop() {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-l.done:
			return
		case now := <-t.C:
			l.mu.Lock()
			g, ok := l.det.Tick(now)
			l.mu.Unlock()
			l.emit(g, ok)
		}
	}
}

// slotState is one multitouch slot's contact.
type slotState struct {
	tracking bool
	x, y     int32
}

// readLoop parses the raw event stream: multitouch protocol B, slot 0 carries
// the gesture, additional active slots just disqualify it.
func (l *Listener) readLoop() {
	var (
		slots [maxSlots]slotState
		cur   int
		buf   = make([]byte, eventSize*64)
	)
	for {
		n, err := l.f.Read(buf)
		if err != nil {
			select {
			case <-l.done: // closed by us
			default:
				l.log.Error("touch device read failed; trigger listening stopped", "err", err)
			}
			return
		}
		for o := 0; o+eventSize <= n; o += eventSize {
			sec := int32(binary.LittleEndian.Uint32(buf[o:]))
			usec := int32(binary.LittleEndian.Uint32(buf[o+4:]))
			typ := binary.LittleEndian.Uint16(buf[o+8:])
			code := binary.LittleEndian.Uint16(buf[o+10:])
			val := int32(binary.LittleEndian.Uint32(buf[o+12:]))

			switch typ {
			case evAbs:
				switch code {
				case absMtSlot:
					if val >= 0 && int(val) < maxSlots {
						cur = int(val)
					}
				case absMtTrackingID:
					slots[cur].tracking = val >= 0
				case absMtPositionX:
					slots[cur].x = val
				case absMtPositionY:
					slots[cur].y = val
				}
			case evSyn:
				if code != synReport {
					break
				}
				t := time.Unix(int64(sec), int64(usec)*1000)
				if sec == 0 {
					t = time.Now() // defensive: unstamped event
				}
				fingers := 0
				for i := range slots {
					if slots[i].tracking {
						fingers++
					}
				}
				p := geom.Point{
					X: float64(slots[0].x),
					Y: float64(touchMaxY - slots[0].y),
				}
				l.mu.Lock()
				g, ok := l.det.Frame(t, fingers, p)
				l.mu.Unlock()
				l.emit(g, ok)
			}
		}
	}
}
