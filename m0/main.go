// m0 — reMarkable 2 M0 verification tool.
//
// Modes:
//
//	m0 info -dev /dev/input/event1              print device name + ABS axis ranges
//	m0 pen -dev ... -x0 -y0 -x1 -y1 [...]       inject a pen stroke (straight line)
//	m0 swipe -dev ... -x0 -y0 -x1 -y1 [...]     inject a one-finger MT-B swipe
//	m0 record -dev ... -sec 5                   dump incoming events (for calibration)
//
// Build: GOOS=linux GOARCH=arm GOARM=7 go build -o m0 .
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	evSyn = 0x00
	evKey = 0x01
	evAbs = 0x03

	synReport = 0

	btnToolPen    = 0x140
	btnToolFinger = 0x145
	btnTouch      = 0x14a

	absX          = 0x00
	absY          = 0x01
	absPressure   = 0x18
	absDistance   = 0x19
	absTiltX      = 0x1a
	absTiltY      = 0x1b
	absMtSlot     = 0x2f
	absMtPosX     = 0x35
	absMtPosY     = 0x36
	absMtTrackID  = 0x39
	absMtPressure = 0x3a
)

var absNames = map[int]string{
	absX: "ABS_X", absY: "ABS_Y",
	absPressure: "ABS_PRESSURE", absDistance: "ABS_DISTANCE",
	absTiltX: "ABS_TILT_X", absTiltY: "ABS_TILT_Y",
	absMtSlot: "ABS_MT_SLOT", 0x30: "ABS_MT_TOUCH_MAJOR", 0x31: "ABS_MT_TOUCH_MINOR",
	0x34: "ABS_MT_ORIENTATION", absMtPosX: "ABS_MT_POSITION_X", absMtPosY: "ABS_MT_POSITION_Y",
	0x37: "ABS_MT_TOOL_TYPE", absMtTrackID: "ABS_MT_TRACKING_ID", absMtPressure: "ABS_MT_PRESSURE",
}

// input_event on 32-bit ARM: timeval{int32,int32} + u16 type + u16 code + s32 value = 16 bytes.
// Timestamp left zero: the kernel stamps injected events itself.
func emit(f *os.File, typ, code uint16, val int32) {
	var b [16]byte
	binary.LittleEndian.PutUint16(b[8:10], typ)
	binary.LittleEndian.PutUint16(b[10:12], code)
	binary.LittleEndian.PutUint32(b[12:16], uint32(val))
	if _, err := f.Write(b[:]); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
}

func report(f *os.File) { emit(f, evSyn, synReport, 0) }

func ioctlRead(fd uintptr, nr, size int, ptr unsafe.Pointer) error {
	req := uintptr(2)<<30 | uintptr(size)<<16 | uintptr('E')<<8 | uintptr(nr)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(ptr)); errno != 0 {
		return errno
	}
	return nil
}

func infoCmd(dev string) {
	f, err := os.Open(dev)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	fd := f.Fd()

	var name [256]byte
	ioctlRead(fd, 0x06, len(name), unsafe.Pointer(&name[0])) // EVIOCGNAME
	fmt.Printf("%s  name=%q\n", dev, strings.TrimRight(string(name[:]), "\x00"))

	var bits [8]byte
	if err := ioctlRead(fd, 0x20+evAbs, len(bits), unsafe.Pointer(&bits[0])); err != nil { // EVIOCGBIT(EV_ABS)
		fmt.Fprintln(os.Stderr, "EVIOCGBIT:", err)
		return
	}
	for code := 0; code < 64; code++ {
		if bits[code/8]&(1<<(code%8)) == 0 {
			continue
		}
		var ai [6]int32 // value, min, max, fuzz, flat, resolution
		if err := ioctlRead(fd, 0x40+code, 24, unsafe.Pointer(&ai[0])); err != nil { // EVIOCGABS
			continue
		}
		n := absNames[code]
		if n == "" {
			n = fmt.Sprintf("ABS_0x%02x", code)
		}
		fmt.Printf("  %-20s value=%-6d min=%-6d max=%-6d\n", n, ai[0], ai[1], ai[2])
	}
}

func penCmd(dev string, x0, y0, x1, y1, points, delayMs, pressure int, richHover bool) {
	f, err := os.OpenFile(dev, os.O_WRONLY, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	emit(f, evKey, btnToolPen, 1)
	report(f)
	time.Sleep(50 * time.Millisecond)

	if richHover {
		// simulate a real pen approach: distance ramps 90 -> 0 with tilt and
		// slight position drift, ~300ms, mirroring what the digitizer streams
		// while a human lowers the pen onto the surface
		for i := 0; i < 20; i++ {
			emit(f, evAbs, absX, int32(x0+(20-i)*3))
			emit(f, evAbs, absY, int32(y0+(20-i)*2))
			emit(f, evAbs, absDistance, int32(90-i*4-int(i/2)))
			emit(f, evAbs, absTiltX, 300)
			emit(f, evAbs, absTiltY, 500)
			report(f)
			time.Sleep(15 * time.Millisecond)
		}
	}

	// hover to the start point before touching down
	emit(f, evAbs, absX, int32(x0))
	emit(f, evAbs, absY, int32(y0))
	emit(f, evAbs, absDistance, 0)
	report(f)
	time.Sleep(20 * time.Millisecond)

	emit(f, evKey, btnTouch, 1)
	emit(f, evAbs, absPressure, int32(pressure))
	report(f)

	for i := 0; i <= points; i++ {
		emit(f, evAbs, absX, int32(x0+(x1-x0)*i/points))
		emit(f, evAbs, absY, int32(y0+(y1-y0)*i/points))
		emit(f, evAbs, absPressure, int32(pressure))
		report(f)
		time.Sleep(time.Duration(delayMs) * time.Millisecond)
	}

	emit(f, evAbs, absPressure, 0)
	emit(f, evKey, btnTouch, 0)
	report(f)
	if richHover { // lift away: distance ramps back up
		for i := 0; i < 8; i++ {
			emit(f, evAbs, absDistance, int32(10+i*10))
			report(f)
			time.Sleep(15 * time.Millisecond)
		}
	}
	emit(f, evKey, btnToolPen, 0)
	report(f)
	fmt.Printf("pen stroke done: (%d,%d)->(%d,%d) points=%d delay=%dms hover=%v\n", x0, y0, x1, y1, points, delayMs, richHover)
}

// triggerCmd — gesture detector prototype: watches the touch device for a
// double-tap inside rect [x0,y0]-[x1,y1] (touch coords). Prints TRIGGER on hit.
func triggerCmd(dev string, sec, x0, y0, x1, y1 int) {
	f, err := os.Open(dev)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	go func() {
		time.Sleep(time.Duration(sec) * time.Second)
		os.Exit(0)
	}()

	const (
		tapMaxMs  = 300 // touch down->up longer than this is not a tap
		gapMaxMs  = 600 // max delay between the two taps
		distMaxPx = 120 // max travel of the two taps' positions
	)
	var (
		down      bool
		downAt    time.Time
		cx, cy    int32
		lastTapAt time.Time
		lastTapX  int32
		lastTapY  int32
		haveTap   bool
	)
	inRect := func(x, y int32) bool {
		return x >= int32(x0) && x <= int32(x1) && y >= int32(y0) && y <= int32(y1)
	}
	buf := make([]byte, 16*64)
	for {
		n, err := f.Read(buf)
		if err != nil {
			return
		}
		for o := 0; o+16 <= n; o += 16 {
			typ := binary.LittleEndian.Uint16(buf[o+8:])
			code := binary.LittleEndian.Uint16(buf[o+10:])
			val := int32(binary.LittleEndian.Uint32(buf[o+12:]))
			if typ != evAbs {
				continue
			}
			switch code {
			case absMtPosX:
				cx = val
			case absMtPosY:
				cy = val
			case absMtTrackID:
				if val >= 0 {
					down, downAt = true, time.Now()
				} else if down {
					down = false
					dur := time.Since(downAt)
					if dur < tapMaxMs*time.Millisecond && inRect(cx, cy) {
						now := time.Now()
						if haveTap && now.Sub(lastTapAt) < gapMaxMs*time.Millisecond &&
							abs32(cx-lastTapX) < distMaxPx && abs32(cy-lastTapY) < distMaxPx {
							fmt.Printf("TRIGGER double-tap at (%d,%d)\n", cx, cy)
							haveTap = false
						} else {
							haveTap, lastTapAt, lastTapX, lastTapY = true, now, cx, cy
							fmt.Printf("tap at (%d,%d) dur=%dms\n", cx, cy, dur.Milliseconds())
						}
					} else {
						haveTap = false // long press or out of zone resets
					}
				}
			}
		}
	}
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func swipeCmd(dev string, x0, y0, x1, y1, points, delayMs int, useBtn bool) {
	f, err := os.OpenFile(dev, os.O_WRONLY, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	emit(f, evAbs, absMtSlot, 0)
	emit(f, evAbs, absMtTrackID, 1)
	if useBtn {
		emit(f, evKey, btnTouch, 1)
		emit(f, evKey, btnToolFinger, 1)
	}
	emit(f, evAbs, absMtPosX, int32(x0))
	emit(f, evAbs, absMtPosY, int32(y0))
	report(f)

	for i := 1; i <= points; i++ {
		emit(f, evAbs, absMtPosX, int32(x0+(x1-x0)*i/points))
		emit(f, evAbs, absMtPosY, int32(y0+(y1-y0)*i/points))
		report(f)
		time.Sleep(time.Duration(delayMs) * time.Millisecond)
	}

	emit(f, evAbs, absMtTrackID, -1)
	if useBtn {
		emit(f, evKey, btnTouch, 0)
		emit(f, evKey, btnToolFinger, 0)
	}
	report(f)
	fmt.Printf("swipe done: (%d,%d)->(%d,%d)\n", x0, y0, x1, y1)
}

func recordCmd(dev string, sec int) {
	f, err := os.Open(dev)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	go func() {
		time.Sleep(time.Duration(sec) * time.Second)
		os.Exit(0)
	}()
	typNames := map[uint16]string{evSyn: "SYN", evKey: "KEY", evAbs: "ABS"}
	buf := make([]byte, 16*64)
	for {
		n, err := f.Read(buf)
		if err != nil {
			return
		}
		for o := 0; o+16 <= n; o += 16 {
			typ := binary.LittleEndian.Uint16(buf[o+8:])
			code := binary.LittleEndian.Uint16(buf[o+10:])
			val := int32(binary.LittleEndian.Uint32(buf[o+12:]))
			tn := typNames[typ]
			if tn == "" {
				tn = fmt.Sprintf("T%d", typ)
			}
			cn := absNames[int(code)]
			if typ == evKey {
				switch code {
				case btnToolPen:
					cn = "BTN_TOOL_PEN"
				case btnTouch:
					cn = "BTN_TOUCH"
				case btnToolFinger:
					cn = "BTN_TOOL_FINGER"
				}
			}
			if cn == "" {
				cn = fmt.Sprintf("0x%02x", code)
			}
			fmt.Printf("%s %-20s %d\n", tn, cn, val)
		}
	}
}

// --- minimal single-stroke demo font (10x14 grid, y down) ---

type fpt struct{ X, Y float64 }

var miniFont = map[rune][][]fpt{
	'H': {{{0, 0}, {0, 14}}, {{10, 0}, {10, 14}}, {{0, 7}, {10, 7}}},
	'E': {{{10, 0}, {0, 0}, {0, 14}, {10, 14}}, {{0, 7}, {6, 7}}},
	'L': {{{0, 0}, {0, 14}, {10, 14}}},
	'O': {{{3, 0}, {7, 0}, {10, 3}, {10, 11}, {7, 14}, {3, 14}, {0, 11}, {0, 3}, {3, 0}}},
	'W': {{{0, 0}, {2, 14}, {5, 5}, {8, 14}, {10, 0}}},
	'R': {{{0, 14}, {0, 0}, {8, 0}, {10, 2}, {10, 5}, {8, 7}, {0, 7}}, {{5, 7}, {10, 14}}},
	'D': {{{0, 0}, {0, 14}}, {{0, 0}, {7, 0}, {10, 3}, {10, 11}, {7, 14}, {0, 14}}},
	'I': {{{3, 0}, {7, 0}}, {{5, 0}, {5, 14}}, {{3, 14}, {7, 14}}},
	'A': {{{0, 14}, {5, 0}, {10, 14}}, {{2, 9}, {8, 9}}},
	'M': {{{0, 14}, {0, 0}, {5, 7}, {10, 0}, {10, 14}}},
	'2': {{{0, 3}, {2, 0}, {8, 0}, {10, 3}, {10, 5}, {0, 14}, {10, 14}}},
}

// screen pixels (portrait 1404x1872) -> wacom coords, per M0 calibration
func toWacom(sx, sy float64) (int32, int32) {
	wx := (1 - sy/1872.0) * 20966
	wy := sx * 15725.0 / 1404.0
	if wx < 0 {
		wx = 0
	}
	if wx > 20966 {
		wx = 20966
	}
	if wy < 0 {
		wy = 0
	}
	if wy > 15725 {
		wy = 15725
	}
	return int32(wx), int32(wy)
}

// dynPressure enables a calligraphic pressure envelope: fast attack at the
// stroke start (起笔顿), full pressure through the body, tapered release over
// the last third (收笔提锋). xochitl's pressure-sensitive brushes turn this
// into visible width variation.
var dynPressure bool

func drawStroke(f *os.File, pts []fpt, delayMs, pressure int) {
	total := 0.0
	for i := 1; i < len(pts); i++ {
		total += math.Hypot(pts[i].X-pts[i-1].X, pts[i].Y-pts[i-1].Y)
	}
	press := func(frac float64) int32 {
		if !dynPressure || total < 12 { // dots and tiny strokes: constant
			return int32(pressure)
		}
		switch {
		case frac < 0.10:
			return int32(float64(pressure) * (0.55 + 4.5*frac))
		case frac < 0.70:
			return int32(pressure)
		default:
			return int32(float64(pressure) * (1.0 - 0.72*(frac-0.70)/0.30))
		}
	}

	wx, wy := toWacom(pts[0].X, pts[0].Y)
	emit(f, evAbs, absX, wx)
	emit(f, evAbs, absY, wy)
	report(f)
	time.Sleep(15 * time.Millisecond)
	emit(f, evKey, btnTouch, 1)
	emit(f, evAbs, absPressure, press(0))
	report(f)
	traveled := 0.0
	for i := 1; i < len(pts); i++ {
		dx, dy := pts[i].X-pts[i-1].X, pts[i].Y-pts[i-1].Y
		seg := math.Hypot(dx, dy)
		n := int(seg/3.0) + 1
		for j := 1; j <= n; j++ {
			wx, wy := toWacom(pts[i-1].X+dx*float64(j)/float64(n), pts[i-1].Y+dy*float64(j)/float64(n))
			frac := 0.0
			if total > 0 {
				frac = (traveled + seg*float64(j)/float64(n)) / total
			}
			emit(f, evAbs, absX, wx)
			emit(f, evAbs, absY, wy)
			emit(f, evAbs, absPressure, press(frac))
			report(f)
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}
		traveled += seg
	}
	emit(f, evAbs, absPressure, 0)
	emit(f, evKey, btnTouch, 0)
	report(f)
	time.Sleep(15 * time.Millisecond)
}

func textCmd(dev, text string, x0, y0, sizePx, delayMs, pressure int) {
	f, err := os.OpenFile(dev, os.O_WRONLY, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	scale := float64(sizePx) / 14.0
	emit(f, evKey, btnToolPen, 1)
	report(f)
	time.Sleep(50 * time.Millisecond)

	penX := float64(x0)
	for _, ch := range strings.ToUpper(text) {
		glyph, ok := miniFont[ch]
		if !ok { // space and unknown chars just advance
			penX += 9 * scale
			continue
		}
		for _, stroke := range glyph {
			pts := make([]fpt, len(stroke))
			for i, p := range stroke {
				pts[i] = fpt{penX + p.X*scale, float64(y0) + p.Y*scale}
			}
			drawStroke(f, pts, delayMs, pressure)
		}
		penX += 13 * scale
	}

	emit(f, evKey, btnToolPen, 0)
	report(f)
	fmt.Printf("text done: %q at (%d,%d) size=%dpx\n", text, x0, y0, sizePx)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: m0 {info|pen|swipe|record} -dev /dev/input/eventX [...]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dev := fs.String("dev", "", "input device path")
	x0 := fs.Int("x0", 0, "start x")
	y0 := fs.Int("y0", 0, "start y")
	x1 := fs.Int("x1", 0, "end x")
	y1 := fs.Int("y1", 0, "end y")
	points := fs.Int("points", 100, "interpolation points")
	delay := fs.Int("delay", 5, "per-point delay ms")
	pressure := fs.Int("pressure", 2000, "pen pressure")
	sec := fs.Int("sec", 5, "record duration seconds")
	btn := fs.Bool("btn", false, "swipe: also send BTN_TOUCH/BTN_TOOL_FINGER")
	text := fs.String("text", "", "text to write (text mode)")
	size := fs.Int("size", 80, "glyph height in screen px (text mode)")
	font := fs.String("font", "hershey", "text mode font: hershey | mini")
	file := fs.String("file", "", "SVG file path (svg mode)")
	width := fs.Int("width", 400, "drawing width in screen px (svg mode)")
	dyn := fs.Bool("dyn", false, "dynamic pressure envelope (calligraphic strokes)")
	hover := fs.Bool("hover", false, "pen: rich hover landing sequence (simulate real approach)")
	fs.Parse(os.Args[2:])
	if *dev == "" {
		fmt.Fprintln(os.Stderr, "-dev is required")
		os.Exit(2)
	}
	dynPressure = *dyn
	switch os.Args[1] {
	case "info":
		infoCmd(*dev)
	case "pen":
		penCmd(*dev, *x0, *y0, *x1, *y1, *points, *delay, *pressure, *hover)
	case "trigger":
		triggerCmd(*dev, *sec, *x0, *y0, *x1, *y1)
	case "swipe":
		swipeCmd(*dev, *x0, *y0, *x1, *y1, *points, *delay, *btn)
	case "record":
		recordCmd(*dev, *sec)
	case "svg":
		svgCmd(*dev, *file, *x0, *y0, *width, *delay, *pressure)
	case "preview":
		previewCmd(*file, *dev, *width) // -dev doubles as output path here
	case "text":
		if *font == "hershey" {
			textHershey(*dev, *text, *x0, *y0, *size, *delay, *pressure)
		} else {
			textCmd(*dev, *text, *x0, *y0, *size, *delay, *pressure)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown mode:", os.Args[1])
		os.Exit(2)
	}
}
