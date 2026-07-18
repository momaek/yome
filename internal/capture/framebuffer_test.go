package capture

import (
	"encoding/binary"
	"image"
	"strings"
	"testing"
)

// A trimmed /proc/<pid>/maps in the shape 3.11 produces: the fb0 mapping
// followed by the anonymous region holding the real frame.
const maps311 = `00010000-00c46000 r-xp 00000000 b3:03 1234       /usr/bin/xochitl
b6d00000-b6d40000 rw-s 00000000 00:0d 5678       /dev/fb0
b6e00000-b6f00000 rw-p 00000000 00:00 0
b7000000-b7100000 rw-p 00000000 00:00 0
`

// The 3.27 shape: the line right after /dev/fb0 is a *named* region (Qt heap),
// and that is the one holding the frame. "First anonymous" would skip it.
const maps327 = `00010000-00c46000 r-xp 00000000 b3:03 1234       /usr/bin/xochitl
b6d00000-b6d40000 rw-s 00000000 00:0d 5678       /dev/fb0
b6e00000-b7a00000 rw-p 00000000 00:00 0          [anon:qt_heap]
b7b00000-b7c00000 rw-p 00000000 00:00 0
`

func TestParseMapsAnonAfterFB0(t *testing.T) {
	got, err := parseMaps(strings.NewReader(maps311), Spec311)
	if err != nil {
		t.Fatal(err)
	}
	// The frame sits Offset bytes into the first anonymous region after fb0.
	if want := uint64(0xb6e00000 + 8); got != want {
		t.Errorf("frame address = %#x, want %#x", got, want)
	}
}

func TestParseMapsNextAfterFB0(t *testing.T) {
	got, err := parseMaps(strings.NewReader(maps327), Spec327)
	if err != nil {
		t.Fatal(err)
	}
	// 3.27 takes the very next region regardless of its name.
	if want := uint64(0xb6e00000 + 2629632 + 8); got != want {
		t.Errorf("frame address = %#x, want %#x", got, want)
	}
}

func TestParseMapsAnonSkipsNamedRegions(t *testing.T) {
	// On the 3.27-shaped layout the region right after fb0 is *named*, so the
	// anon method must skip it — which is exactly why 3.27 needs the
	// next_after_fb0 method to find the true frame region.
	got, err := parseMaps(strings.NewReader(maps327), Spec311)
	if err != nil {
		t.Fatal(err)
	}
	if want := uint64(0xb7b00000 + 8); got != want {
		t.Errorf("anon method picked %#x, want it to skip the named region to %#x", got, want)
	}
}

func TestParseMapsFailsLoudly(t *testing.T) {
	tests := []struct {
		name string
		spec Spec
		maps string
		want string
	}{{
		name: "no fb0 mapping at all",
		spec: Spec311,
		maps: "00010000-00c46000 r-xp 00000000 b3:03 1234 /usr/bin/xochitl\n",
		want: "fb0",
	}, {
		// This is the shape a firmware change would take: the mapping is
		// there but the layout after it is not what we calibrated against.
		name: "no anonymous region after fb0",
		spec: Spec311,
		maps: "b6d00000-b6d40000 rw-s 00000000 00:0d 5678 /dev/fb0\n" +
			"b6e00000-b6f00000 r-xp 00000000 b3:03 9 /usr/lib/libQt5Gui.so\n",
		want: "anonymous",
	}, {
		name: "fb0 is the last mapping",
		spec: Spec327,
		maps: "b6d00000-b6d40000 rw-s 00000000 00:0d 5678 /dev/fb0\n",
		want: "region after",
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseMaps(strings.NewReader(tc.maps), tc.spec)
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestSpecValidate(t *testing.T) {
	for _, s := range []Spec{Spec311, Spec327} {
		if err := s.Validate(); err != nil {
			t.Errorf("shipped spec %+v does not validate: %v", s, err)
		}
	}

	bad := Spec311
	bad.RawW, bad.RawH = 100, 100 // transpose3 must map exactly onto 1404x1872
	if err := bad.Validate(); err == nil {
		t.Error("mismatched raw dimensions validated")
	}
	bad = Spec327
	bad.Format = "rgb565"
	if err := bad.Validate(); err == nil {
		t.Error("unknown pixel format validated")
	}
	bad = Spec327
	bad.Locate = "guess"
	if err := bad.Validate(); err == nil {
		t.Error("unknown locate method validated")
	}
}

func TestU16ToGray(t *testing.T) {
	// The 3.11 frame is u16 with 0=black and 30=white, not rgb565 (M0 finding).
	tests := []struct {
		raw  uint16
		want uint8
	}{
		{0, 0},
		{15, 127},
		{30, 255},
		{99, 255}, // out of range clamps to paper
	}
	for _, tc := range tests {
		if got := u16ToGray(tc.raw); got != tc.want {
			t.Errorf("u16ToGray(%d) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestBGRAGray(t *testing.T) {
	tests := []struct {
		name    string
		b, g, r uint8
		want    uint8
	}{
		{"black", 0, 0, 0, 0},
		{"white", 255, 255, 255, 255},
		{"pure blue is dark", 255, 0, 0, 29},
		{"pure green dominates", 0, 255, 0, 149},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := []byte{tc.b, tc.g, tc.r, 0xff}
			if got := Spec327.gray(buf, 0); got != tc.want {
				t.Errorf("gray(B=%d G=%d R=%d) = %d, want %d", tc.b, tc.g, tc.r, got, tc.want)
			}
		})
	}
}

func TestRawIndexIsWithinTheFrame(t *testing.T) {
	for _, spec := range []Spec{Spec311, Spec327} {
		for _, p := range [][2]int{{0, 0}, {ScreenW - 1, 0}, {0, ScreenH - 1}, {ScreenW - 1, ScreenH - 1}} {
			i := spec.rawIndex(p[0], p[1])
			if i < 0 || i >= int64(spec.RawW*spec.RawH) {
				t.Errorf("%s: rawIndex%v = %d, outside the %d-sample frame", spec.Rotation, p, i, spec.RawW*spec.RawH)
			}
		}
	}
}

func TestRawIndexIsABijection(t *testing.T) {
	// Every screen pixel must map to a distinct raw sample; an overlap would
	// mean the rotation is dropping or duplicating rows.
	for _, spec := range []Spec{Spec311, Spec327} {
		seen := make(map[int64]bool, ScreenW*ScreenH)
		for y := 0; y < ScreenH; y++ {
			for x := 0; x < ScreenW; x++ {
				i := spec.rawIndex(x, y)
				if seen[i] {
					t.Fatalf("%s: raw sample %d claimed twice, at screen (%d,%d)", spec.Rotation, i, x, y)
				}
				seen[i] = true
			}
		}
		if len(seen) != spec.RawW*spec.RawH {
			t.Errorf("%s: covered %d of %d raw samples", spec.Rotation, len(seen), spec.RawW*spec.RawH)
		}
	}
}

// blankFrame builds an all-white raw frame for spec.
func blankFrame(spec Spec) []byte {
	buf := make([]byte, spec.FrameBytes())
	switch spec.Format {
	case FormatU16Gray:
		for i := 0; i < len(buf); i += 2 {
			binary.LittleEndian.PutUint16(buf[i:], grayMax)
		}
	case FormatBGRA8888:
		for i := range buf {
			buf[i] = 0xff
		}
	}
	return buf
}

// markRaw blackens one raw sample.
func markRaw(spec Spec, buf []byte, rx, ry int) {
	i := ry*spec.RawW + rx
	switch spec.Format {
	case FormatU16Gray:
		binary.LittleEndian.PutUint16(buf[i*2:], 0)
	case FormatBGRA8888:
		copy(buf[i*4:], []byte{0, 0, 0, 0xff})
	}
}

// TestDecodeFrameOrientation pins the portrait mapping per rotation. For
// transpose3 (3.11): raw is landscape and the view is a 90° clockwise turn
// plus a vertical flip (ffmpeg transpose=3, M0 calibration). Getting this
// wrong yields a screenshot that is mirrored or sideways.
func TestDecodeFrameOrientation(t *testing.T) {
	tests := []struct {
		name         string
		spec         Spec
		rawX, rawY   int
		wantX, wantY int
	}{
		{"3.11 raw bottom-right maps to screen top-left", Spec311, Spec311.RawW - 1, Spec311.RawH - 1, 0, 0},
		{"3.11 raw bottom-left maps to screen top-right", Spec311, 0, Spec311.RawH - 1, 0, ScreenH - 1},
		{"3.27 raw is already portrait", Spec327, 3, 7, 3, 7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := blankFrame(tc.spec)
			markRaw(tc.spec, buf, tc.rawX, tc.rawY)
			img := decodeFrame(tc.spec, buf)
			if got := img.GrayAt(tc.wantX, tc.wantY).Y; got != 0 {
				t.Errorf("screen (%d,%d) = %d, want the marked 0", tc.wantX, tc.wantY, got)
			}
			if n := CountInk(img); n != 1 {
				t.Errorf("one marked sample produced %d ink pixels", n)
			}
		})
	}
}

func TestDecodeFrameDimensions(t *testing.T) {
	for _, spec := range []Spec{Spec311, Spec327} {
		img := decodeFrame(spec, make([]byte, spec.FrameBytes()))
		if got := img.Bounds().Size(); got.X != ScreenW || got.Y != ScreenH {
			t.Errorf("%s: decoded image is %v, want the %dx%d portrait screen", spec.Rotation, got, ScreenW, ScreenH)
		}
	}
}

func TestRawRowRange(t *testing.T) {
	tests := []struct {
		name     string
		spec     Spec
		rect     image.Rectangle
		min, max int
	}{
		{"none: rows are portrait rows", Spec327, image.Rect(0, 112, 110, 222), 112, 221},
		// transpose3: portrait x selects the raw row, row = RawH-1-x.
		{"transpose3: rows come from portrait x", Spec311, image.Rect(0, 112, 110, 222), Spec311.RawH - 110, Spec311.RawH - 1},
		{"transpose3: single column", Spec311, image.Rect(55, 0, 56, 10), Spec311.RawH - 56, Spec311.RawH - 56},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotMin, gotMax := tc.spec.rawRowRange(tc.rect)
			if gotMin != tc.min || gotMax != tc.max {
				t.Errorf("rawRowRange(%v) = [%d,%d], want [%d,%d]", tc.rect, gotMin, gotMax, tc.min, tc.max)
			}
			// The range must cover every sample the rect touches.
			for y := tc.rect.Min.Y; y < tc.rect.Max.Y; y++ {
				for x := tc.rect.Min.X; x < tc.rect.Max.X; x++ {
					row := int(tc.spec.rawIndex(x, y)) / tc.spec.RawW
					if row < gotMin || row > gotMax {
						t.Fatalf("pixel (%d,%d) lives in raw row %d, outside [%d,%d]", x, y, row, gotMin, gotMax)
					}
				}
			}
		})
	}
}

func TestCountInk(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 10, 10))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	if got := CountInk(img); got != 0 {
		t.Errorf("a blank page counted %d ink pixels", got)
	}

	for i := 0; i < 7; i++ {
		img.Pix[i] = 0
	}
	if got := CountInk(img); got != 7 {
		t.Errorf("CountInk = %d, want 7", got)
	}
	// Mid-gray is ink; paper-white is not.
	img.Pix[20] = BlackThreshold - 1
	img.Pix[21] = BlackThreshold
	if got := CountInk(img); got != 8 {
		t.Errorf("CountInk = %d, want 8 (the threshold is exclusive)", got)
	}
}

func TestCountInkRect(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 10, 10))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	img.SetGray(1, 1, image.NewGray(image.Rect(0, 0, 1, 1)).GrayAt(0, 0)) // black

	if got := CountInkRect(img, image.Rect(0, 0, 5, 5)); got != 1 {
		t.Errorf("ink in the top-left quarter = %d, want 1", got)
	}
	if got := CountInkRect(img, image.Rect(5, 5, 10, 10)); got != 0 {
		t.Errorf("ink in the bottom-right quarter = %d, want 0", got)
	}
	// An out-of-bounds rect must clip, not panic.
	if got := CountInkRect(img, image.Rect(-100, -100, 100, 100)); got != 1 {
		t.Errorf("ink over an oversized rect = %d, want 1", got)
	}
}
