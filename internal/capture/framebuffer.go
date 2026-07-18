// Package capture reads the screen out of xochitl's process memory.
//
// The rM2 renders privately and never publishes a readable image to /dev/fb0,
// so the frame is located through /proc/<pid>/maps relative to the /dev/fb0
// mapping. Where it sits and how pixels are encoded changed with the Qt6
// rewrite, so both are described by a Spec and calibrated per firmware:
//
//   - 3.11.2.5 (M0): first anonymous region after /dev/fb0, +8. u16le samples,
//     0=black 30=white (not rgb565 as older community docs claim). The raw
//     frame is landscape; portrait is transpose=3 (rotate 90° CW + vflip).
//   - 3.27.3.0 (Qt6): the region on the very next maps line after /dev/fb0,
//     +2629632+8 (goMarkableStream 3.24+ method). BGRA8888, already portrait.
//
// This is the project's one dependency on xochitl's internals; everything else
// rides on stable kernel interfaces. The Spec ships inside each firmware's UI
// map, so a new firmware is a calibration entry, not a rebuild.
package capture

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Locate methods: how to find the frame region in /proc/<pid>/maps.
const (
	// LocateAnonAfterFB0 takes the first anonymous region after the /dev/fb0
	// mapping (firmware 3.11.2.5).
	LocateAnonAfterFB0 = "anon_after_fb0"
	// LocateNextAfterFB0 takes whatever region the next maps line describes
	// (firmware 3.27.3.0; the frame moved into a named Qt heap region, so
	// "first anonymous" finds the wrong one there).
	LocateNextAfterFB0 = "next_after_fb0"
)

// Pixel formats.
const (
	FormatU16Gray  = "u16gray"  // u16le, 0=black .. 30=white
	FormatBGRA8888 = "bgra8888" // 4 bytes per pixel: B,G,R,A
)

// Rotations from the raw buffer to the portrait view.
const (
	RotationNone       = "none"       // raw is already portrait
	RotationTranspose3 = "transpose3" // rotate 90° clockwise, then flip vertically
)

// grayMax is the white level of the u16gray format.
const grayMax = 30

// ProcessName is the xochitl binary's name as it appears in /proc/*/comm.
const ProcessName = "xochitl"

// Spec describes where a firmware keeps its frame and how it is encoded.
type Spec struct {
	Locate   string // LocateAnonAfterFB0 | LocateNextAfterFB0
	Offset   int64  // from the located region's start to the frame data
	Format   string // FormatU16Gray | FormatBGRA8888
	RawW     int    // raw buffer width in pixels
	RawH     int    // raw buffer height in pixels
	Rotation string // RotationNone | RotationTranspose3
}

// Spec311 is the frame layout of firmware 3.11.2.5, measured in M0.
var Spec311 = Spec{
	Locate:   LocateAnonAfterFB0,
	Offset:   8,
	Format:   FormatU16Gray,
	RawW:     1872,
	RawH:     1404,
	Rotation: RotationTranspose3,
}

// Spec327 is the frame layout of firmware 3.27.3.0 (Qt6), measured 2026-07-17.
var Spec327 = Spec{
	Locate:   LocateNextAfterFB0,
	Offset:   2629632 + 8,
	Format:   FormatBGRA8888,
	RawW:     1404,
	RawH:     1872,
	Rotation: RotationNone,
}

// Validate checks the spec is internally consistent before any device IO.
func (s Spec) Validate() error {
	var errs []error
	switch s.Locate {
	case LocateAnonAfterFB0, LocateNextAfterFB0:
	default:
		errs = append(errs, fmt.Errorf("unknown locate method %q", s.Locate))
	}
	switch s.Format {
	case FormatU16Gray, FormatBGRA8888:
	default:
		errs = append(errs, fmt.Errorf("unknown pixel format %q", s.Format))
	}
	switch s.Rotation {
	case RotationNone:
		if s.RawW != ScreenW || s.RawH != ScreenH {
			errs = append(errs, fmt.Errorf("rotation %q needs a %dx%d raw frame, got %dx%d",
				s.Rotation, ScreenW, ScreenH, s.RawW, s.RawH))
		}
	case RotationTranspose3:
		if s.RawW != ScreenH || s.RawH != ScreenW {
			errs = append(errs, fmt.Errorf("rotation %q needs a %dx%d raw frame, got %dx%d",
				s.Rotation, ScreenH, ScreenW, s.RawW, s.RawH))
		}
	default:
		errs = append(errs, fmt.Errorf("unknown rotation %q", s.Rotation))
	}
	if s.Offset < 0 {
		errs = append(errs, fmt.Errorf("negative frame offset %d", s.Offset))
	}
	return errors.Join(errs...)
}

// bytesPerPixel of the raw format.
func (s Spec) bytesPerPixel() int {
	if s.Format == FormatBGRA8888 {
		return 4
	}
	return 2
}

// FrameBytes is the size of one raw frame.
func (s Spec) FrameBytes() int { return s.RawW * s.RawH * s.bytesPerPixel() }

// rawIndex maps a portrait screen pixel to its sample index in the raw frame.
//
// For transpose3 the portrait view is the raw frame rotated 90 degrees
// clockwise and flipped vertically, so screen (x,y) reads raw (1871-y, 1403-x).
func (s Spec) rawIndex(x, y int) int64 {
	if s.Rotation == RotationTranspose3 {
		return int64(s.RawH-1-x)*int64(s.RawW) + int64(s.RawW-1-y)
	}
	return int64(y)*int64(s.RawW) + int64(x)
}

// gray decodes the sample at index i of a raw frame buffer to 8-bit gray.
func (s Spec) gray(buf []byte, i int64) uint8 {
	if s.Format == FormatBGRA8888 {
		o := i * 4
		b, g, r := buf[o], buf[o+1], buf[o+2]
		return uint8((299*uint32(r) + 587*uint32(g) + 114*uint32(b)) / 1000)
	}
	return u16ToGray(binary.LittleEndian.Uint16(buf[i*2:]))
}

// u16ToGray converts a u16gray sample (0..30) to an 8-bit gray level.
func u16ToGray(v uint16) uint8 {
	if v >= grayMax {
		return 255
	}
	return uint8(uint32(v) * 255 / grayMax)
}

// Framebuffer is an open handle on xochitl's frame memory. It is cheap to keep
// around and supports full-frame reads, single-pixel probes, and region reads.
type Framebuffer struct {
	PID  int
	Base uint64 // address of the frame within the process

	spec Spec
	mem  *os.File
}

// Open finds xochitl and locates its frame buffer according to spec.
func Open(spec Spec) (*Framebuffer, error) {
	pid, err := FindProcess(ProcessName)
	if err != nil {
		return nil, err
	}
	return OpenPID(pid, spec)
}

// OpenPID locates the frame buffer of a specific process.
func OpenPID(pid int, spec Spec) (*Framebuffer, error) {
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("capture spec: %w", err)
	}
	base, err := locateFrame(pid, spec)
	if err != nil {
		return nil, err
	}
	mem, err := os.Open(fmt.Sprintf("/proc/%d/mem", pid))
	if err != nil {
		return nil, fmt.Errorf("open /proc/%d/mem (need root): %w", pid, err)
	}
	return &Framebuffer{PID: pid, Base: base, spec: spec, mem: mem}, nil
}

// Spec returns the layout this framebuffer was opened with.
func (fb *Framebuffer) Spec() Spec { return fb.spec }

// Close releases the handle.
func (fb *Framebuffer) Close() error {
	if fb.mem == nil {
		return nil
	}
	err := fb.mem.Close()
	fb.mem = nil
	return err
}

// FindProcess returns the pid of the named process.
func FindProcess(name string) (int, error) {
	entries, err := filepath.Glob("/proc/[0-9]*/comm")
	if err != nil {
		return 0, fmt.Errorf("scan /proc: %w", err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(e)
		if err != nil {
			continue // process exited between glob and read
		}
		if strings.TrimSpace(string(b)) != name {
			continue
		}
		pid, err := strconv.Atoi(filepath.Base(filepath.Dir(e)))
		if err != nil {
			continue
		}
		return pid, nil
	}
	return 0, fmt.Errorf("process %q is not running", name)
}

// locateFrame parses /proc/<pid>/maps for the frame region and returns the
// address of the frame data inside it.
func locateFrame(pid int, spec Spec) (uint64, error) {
	path := fmt.Sprintf("/proc/%d/maps", pid)
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s (need root): %w", path, err)
	}
	defer f.Close()

	addr, err := parseMaps(f, spec)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return addr, nil
}

// parseMaps finds the frame address in the contents of a /proc/<pid>/maps file.
func parseMaps(r io.Reader, spec Spec) (uint64, error) {
	var seenFB0 bool
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasSuffix(line, "/dev/fb0") {
			seenFB0 = true
			continue
		}
		if !seenFB0 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if spec.Locate == LocateAnonAfterFB0 && len(fields) > 5 {
			continue // has a pathname column: not anonymous, keep looking
		}
		start, _, ok := strings.Cut(fields[0], "-")
		if !ok {
			return 0, fmt.Errorf("malformed address range %q", fields[0])
		}
		addr, err := strconv.ParseUint(start, 16, 64)
		if err != nil {
			return 0, fmt.Errorf("bad address %q: %w", start, err)
		}
		return addr + uint64(spec.Offset), nil
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("read maps: %w", err)
	}
	if !seenFB0 {
		return 0, errors.New("no /dev/fb0 mapping — is this really xochitl?")
	}
	if spec.Locate == LocateAnonAfterFB0 {
		return 0, errors.New("no anonymous region after the /dev/fb0 mapping (the firmware may have changed the memory layout)")
	}
	return 0, errors.New("no region after the /dev/fb0 mapping (the firmware may have changed the memory layout)")
}

// Pixel reads a single portrait-space pixel as an 8-bit gray level. This is the
// probe path: a handful of bytes off /proc/<pid>/mem, milliseconds, no full
// frame decode.
func (fb *Framebuffer) Pixel(x, y int) (uint8, error) {
	if x < 0 || x >= ScreenW || y < 0 || y >= ScreenH {
		return 0, fmt.Errorf("pixel (%d,%d) is outside the %dx%d screen", x, y, ScreenW, ScreenH)
	}
	bpp := fb.spec.bytesPerPixel()
	buf := make([]byte, bpp)
	off := int64(fb.Base) + fb.spec.rawIndex(x, y)*int64(bpp)
	if _, err := fb.mem.ReadAt(buf, off); err != nil {
		return 0, fmt.Errorf("read pixel (%d,%d) at %#x: %w", x, y, off, err)
	}
	return fb.spec.gray(buf, 0), nil
}

// rawRowRange returns the raw-frame row span [min, max] that covers the
// portrait-space rect r (which must already be clipped to the screen).
func (s Spec) rawRowRange(r image.Rectangle) (int, int) {
	if s.Rotation == RotationTranspose3 {
		// Portrait x selects the raw row: row = RawH-1-x.
		return s.RawH - r.Max.X, s.RawH - 1 - r.Min.X
	}
	return r.Min.Y, r.Max.Y - 1
}

// inkRect reads the portrait-space rect r and counts its pixels darker than
// BlackThreshold. It reads only the raw rows covering the rect — one
// contiguous pread — so probing a toolbar block stays cheap.
func (fb *Framebuffer) inkRect(r image.Rectangle) (ink, total int, err error) {
	r = r.Intersect(image.Rect(0, 0, ScreenW, ScreenH))
	if r.Empty() {
		return 0, 0, fmt.Errorf("region %v lies outside the %dx%d screen", r, ScreenW, ScreenH)
	}
	bpp := int64(fb.spec.bytesPerPixel())
	minRow, maxRow := fb.spec.rawRowRange(r)
	rowBytes := int64(fb.spec.RawW) * bpp
	buf := make([]byte, int64(maxRow-minRow+1)*rowBytes)
	off := int64(fb.Base) + int64(minRow)*rowBytes
	if _, err := fb.mem.ReadAt(buf, off); err != nil {
		return 0, 0, fmt.Errorf("read rows %d..%d of region %v at %#x: %w", minRow, maxRow, r, off, err)
	}

	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			i := fb.spec.rawIndex(x, y) - int64(minRow)*int64(fb.spec.RawW)
			if fb.spec.gray(buf, i) < BlackThreshold {
				ink++
			}
			total++
		}
	}
	return ink, total, nil
}

// InkRatioRect returns the fraction of r's pixels that are ink.
func (fb *Framebuffer) InkRatioRect(r image.Rectangle) (float64, error) {
	ink, total, err := fb.inkRect(r)
	if err != nil {
		return 0, err
	}
	return float64(ink) / float64(total), nil
}

// InkCountRect counts the ink pixels inside r only. Write verification uses
// this over the written area rather than the whole frame: a full-frame count
// also swings when xochitl redraws UI (a keyboard opening mid-write once
// produced a million-pixel "ink" delta with barely a stroke on the page).
func (fb *Framebuffer) InkCountRect(r image.Rectangle) (int, error) {
	ink, _, err := fb.inkRect(r)
	if err != nil {
		return 0, err
	}
	return ink, nil
}
