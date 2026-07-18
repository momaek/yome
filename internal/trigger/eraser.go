package trigger

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/momaek/yome/internal/geom"
)

// Pen digitizer ABI (linux/input-event-codes.h), duplicated like the touch
// constants in listener.go so this side stays self-contained.
const (
	evKey = 0x01

	btnToolRubber = 0x141
	btnTouch      = 0x14a

	absX = 0x00
	absY = 0x01
)

// Wacom digitizer ranges, mirroring inject's calibration (inject.WacomMaxX/Y).
// The digitizer is mounted rotated relative to the portrait screen; see
// inject.ToWacom for the forward mapping.
const (
	wacomMaxX = 20966
	wacomMaxY = 15725
)

// wacomToScreen is the inverse of inject.ToWacom: pen digitizer coordinates
// back to portrait screen space.
func wacomToScreen(x, y int32) geom.Point {
	return geom.Point{
		X: float64(y) * geom.ScreenW / wacomMaxY,
		Y: (1 - float64(x)/wacomMaxX) * geom.ScreenH,
	}
}

// EraseStroke is one continuous eraser-down..up contact of the pen's rubber
// end (Marker Plus and other EMR erasers), in physical screen space. Only the
// official-style electromagnetic eraser is visible here: a capacitive pen
// tail acts on the touchscreen and is indistinguishable from a finger.
type EraseStroke struct {
	Start, End time.Time
	BBox       geom.Rect // bounding box of the contact's travel
	Points     int       // settled frames seen while erasing
}

// EraserDetector accumulates eraser contact frames into strokes. Pure state
// machine like Detector, so it is unit-testable off-device; reading the pen
// device lives in EraserListener.
type EraserDetector struct {
	active                 bool
	start                  time.Time
	minX, minY, maxX, maxY float64
	points                 int
}

// Reset drops a half-built stroke, e.g. when listening resumes after a
// session and must not act on stale state.
func (d *EraserDetector) Reset() { d.active = false }

// Frame consumes one settled pen frame: whether the rubber tool is touching
// the surface at time t, and where. It reports the finished stroke on the
// frame that ends the contact (tool lift or proximity leave).
func (d *EraserDetector) Frame(t time.Time, erasing bool, p geom.Point) (EraseStroke, bool) {
	if erasing {
		if !d.active {
			d.active, d.start, d.points = true, t, 0
			d.minX, d.maxX = p.X, p.X
			d.minY, d.maxY = p.Y, p.Y
		}
		d.minX, d.maxX = math.Min(d.minX, p.X), math.Max(d.maxX, p.X)
		d.minY, d.maxY = math.Min(d.minY, p.Y), math.Max(d.maxY, p.Y)
		d.points++
		return EraseStroke{}, false
	}
	if !d.active {
		return EraseStroke{}, false
	}
	d.active = false
	return EraseStroke{
		Start:  d.start,
		End:    t,
		BBox:   geom.Rect{X: d.minX, Y: d.minY, W: d.maxX - d.minX, H: d.maxY - d.minY},
		Points: d.points,
	}, true
}

// EraserListener reads the pen digitizer and emits eraser strokes.
//
// Like Listener it only reads — the device is never grabbed, so xochitl keeps
// erasing normally. Pause suspends capture while the daemon injects its own
// pen events on this very device (injected strokes carry BTN_TOOL_PEN, never
// the rubber, but symmetric pausing keeps the invariant obvious).
type EraserListener struct {
	f      *os.File
	ch     chan EraseStroke
	paused atomic.Bool
	log    *slog.Logger

	mu  sync.Mutex // guards det
	det *EraserDetector

	done chan struct{}
}

// ListenEraser opens the pen device read-only and starts capturing.
func ListenEraser(devPath string, log *slog.Logger) (*EraserListener, error) {
	if log == nil {
		log = slog.Default()
	}
	f, err := os.Open(devPath)
	if err != nil {
		return nil, fmt.Errorf("open pen device %s: %w", devPath, err)
	}
	l := &EraserListener{
		f:    f,
		ch:   make(chan EraseStroke, 16),
		log:  log,
		det:  &EraserDetector{},
		done: make(chan struct{}),
	}
	go l.readLoop()
	return l, nil
}

// Strokes is the stream of completed eraser strokes. A burst of quick erase
// scrubs buffers up to the channel capacity; beyond that the oldest unread
// strokes are simply the ones the consumer never saw — new ones are dropped
// with a log line rather than blocking the read loop.
func (l *EraserListener) Strokes() <-chan EraseStroke { return l.ch }

// Pause suspends capture and drops any half-built stroke.
func (l *EraserListener) Pause() {
	l.paused.Store(true)
	l.mu.Lock()
	l.det.Reset()
	l.mu.Unlock()
}

// Resume re-enables capture from a clean slate.
func (l *EraserListener) Resume() {
	l.mu.Lock()
	l.det.Reset()
	l.mu.Unlock()
	l.paused.Store(false)
}

// Close stops the read loop and releases the device.
func (l *EraserListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return l.f.Close()
}

// readLoop parses the raw pen event stream. The rubber tool's proximity
// (BTN_TOOL_RUBBER) gates everything: pen-nib events on the same device are
// ignored, and hover frames (rubber near but not touching) end a stroke
// without starting a new one.
func (l *EraserListener) readLoop() {
	var (
		rubber, touch bool
		x, y          int32
		buf           = make([]byte, eventSize*64)
	)
	for {
		n, err := l.f.Read(buf)
		if err != nil {
			select {
			case <-l.done: // closed by us
			default:
				l.log.Error("pen device read failed; eraser capture stopped", "err", err)
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
			case evKey:
				switch code {
				case btnToolRubber:
					rubber = val != 0
				case btnTouch:
					touch = val != 0
				}
			case evAbs:
				switch code {
				case absX:
					x = val
				case absY:
					y = val
				}
			case evSyn:
				if code != synReport {
					break
				}
				t := time.Unix(int64(sec), int64(usec)*1000)
				if sec == 0 {
					t = time.Now() // defensive: unstamped event
				}
				l.mu.Lock()
				s, ok := l.det.Frame(t, rubber && touch, wacomToScreen(x, y))
				l.mu.Unlock()
				l.emitStroke(s, ok)
			}
		}
	}
}

// emitStroke hands a stroke to the channel unless paused, dropping when full.
func (l *EraserListener) emitStroke(s EraseStroke, ok bool) {
	if !ok || l.paused.Load() {
		return
	}
	select {
	case l.ch <- s:
	default:
		l.log.Warn("erase stroke dropped: consumer not keeping up")
	}
}
