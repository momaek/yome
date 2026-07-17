// Package capture reads the screen out of xochitl's process memory.
//
// The rM2 renders privately and never publishes to /dev/fb0, so the frame is
// found the way reSnap does it: locate xochitl's /dev/fb0 mapping in
// /proc/<pid>/maps, take the anonymous region that follows it, and read the
// frame from a fixed offset into it.
//
// This is the project's one dependency on xochitl's internals; everything else
// rides on stable kernel interfaces. Calibrated on firmware 3.11.2.5 (M0).
package capture

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Raw frame geometry. The buffer is landscape; the portrait view the user sees
// is a rotation of it (see Image).
const (
	RawW = 1872
	RawH = 1404

	// bytesPerPixel: the frame is u16 little-endian, NOT rgb565 as older
	// community docs claim. Values run 0 (black) to 30 (white) (M0 finding).
	bytesPerPixel = 2
	// grayMax is the white level of that u16 range.
	grayMax = 30

	// frameOffset is the distance from the anonymous region's start to the
	// frame data.
	frameOffset = 8

	// ProcessName is the xochitl binary's name as it appears in /proc/*/comm.
	ProcessName = "xochitl"
)

// FrameBytes is the size of one raw frame.
const FrameBytes = RawW * RawH * bytesPerPixel

// Framebuffer is an open handle on xochitl's frame memory. It is cheap to keep
// around and supports both full-frame reads and single-pixel probes.
type Framebuffer struct {
	PID  int
	Base uint64 // address of the frame within the process

	mem *os.File
}

// Open finds xochitl and locates its frame buffer.
func Open() (*Framebuffer, error) {
	pid, err := FindProcess(ProcessName)
	if err != nil {
		return nil, err
	}
	return OpenPID(pid)
}

// OpenPID locates the frame buffer of a specific process.
func OpenPID(pid int) (*Framebuffer, error) {
	base, err := locateFrame(pid)
	if err != nil {
		return nil, err
	}
	mem, err := os.Open(fmt.Sprintf("/proc/%d/mem", pid))
	if err != nil {
		return nil, fmt.Errorf("open /proc/%d/mem (need root): %w", pid, err)
	}
	return &Framebuffer{PID: pid, Base: base, mem: mem}, nil
}

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

// locateFrame parses /proc/<pid>/maps for the anonymous region right after the
// /dev/fb0 mapping, and returns the address of the frame data inside it.
func locateFrame(pid int) (uint64, error) {
	path := fmt.Sprintf("/proc/%d/maps", pid)
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s (need root): %w", path, err)
	}
	defer f.Close()

	addr, err := parseMaps(f)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return addr, nil
}

// parseMaps finds the frame address in the contents of a /proc/<pid>/maps file.
func parseMaps(r io.Reader) (uint64, error) {
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
		// The first region after /dev/fb0 with no backing file is the frame.
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) > 5 { // has a pathname column: not anonymous
			continue
		}
		start, _, ok := strings.Cut(fields[0], "-")
		if !ok {
			return 0, fmt.Errorf("malformed address range %q", fields[0])
		}
		addr, err := strconv.ParseUint(start, 16, 64)
		if err != nil {
			return 0, fmt.Errorf("bad address %q: %w", start, err)
		}
		return addr + frameOffset, nil
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("read maps: %w", err)
	}
	if !seenFB0 {
		return 0, errors.New("no /dev/fb0 mapping — is this really xochitl?")
	}
	return 0, errors.New("no anonymous region after the /dev/fb0 mapping (the firmware may have changed the memory layout)")
}

// rawIndex maps a portrait screen pixel to its index in the raw frame.
//
// The portrait view is the raw frame rotated 90 degrees clockwise and flipped
// vertically (ffmpeg transpose=3), so screen (x,y) reads raw (1871-y, 1403-x).
func rawIndex(x, y int) int64 {
	return int64(RawH-1-x)*RawW + int64(RawW-1-y)
}

// Raw reads the whole frame as u16 samples in raw (landscape) order.
func (fb *Framebuffer) Raw() ([]uint16, error) {
	buf := make([]byte, FrameBytes)
	if _, err := fb.mem.ReadAt(buf, int64(fb.Base)); err != nil {
		return nil, fmt.Errorf("read frame at %#x from pid %d: %w", fb.Base, fb.PID, err)
	}
	out := make([]uint16, RawW*RawH)
	for i := range out {
		out[i] = binary.LittleEndian.Uint16(buf[i*2:])
	}
	return out, nil
}

// Pixel reads a single portrait-space pixel as an 8-bit gray level. This is the
// probe path: a couple of bytes off /proc/<pid>/mem, milliseconds, no full
// frame decode.
func (fb *Framebuffer) Pixel(x, y int) (uint8, error) {
	if x < 0 || x >= ScreenW || y < 0 || y >= ScreenH {
		return 0, fmt.Errorf("pixel (%d,%d) is outside the %dx%d screen", x, y, ScreenW, ScreenH)
	}
	var b [2]byte
	off := int64(fb.Base) + rawIndex(x, y)*bytesPerPixel
	if _, err := fb.mem.ReadAt(b[:], off); err != nil {
		return 0, fmt.Errorf("read pixel (%d,%d) at %#x: %w", x, y, off, err)
	}
	return toGray(binary.LittleEndian.Uint16(b[:])), nil
}

// toGray converts a raw u16 sample (0..30) to an 8-bit gray level.
func toGray(v uint16) uint8 {
	if v >= grayMax {
		return 255
	}
	return uint8(uint32(v) * 255 / grayMax)
}
