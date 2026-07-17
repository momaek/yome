package capture

import (
	"image"
	"strings"
	"testing"
)

// A trimmed /proc/<pid>/maps in the shape the rM2 produces: the fb0 mapping
// followed by the anonymous region holding the real frame.
const realisticMaps = `00010000-00c46000 r-xp 00000000 b3:03 1234       /usr/bin/xochitl
b6d00000-b6d40000 rw-s 00000000 00:0d 5678       /dev/fb0
b6e00000-b6f00000 rw-p 00000000 00:00 0
b7000000-b7100000 rw-p 00000000 00:00 0
`

func TestParseMaps(t *testing.T) {
	got, err := parseMaps(strings.NewReader(realisticMaps))
	if err != nil {
		t.Fatal(err)
	}
	// The frame sits frameOffset bytes into the first anonymous region after
	// the fb0 mapping.
	if want := uint64(0xb6e00000 + frameOffset); got != want {
		t.Errorf("frame address = %#x, want %#x", got, want)
	}
}

func TestParseMapsFailsLoudly(t *testing.T) {
	tests := []struct {
		name string
		maps string
		want string
	}{{
		name: "no fb0 mapping at all",
		maps: "00010000-00c46000 r-xp 00000000 b3:03 1234 /usr/bin/xochitl\n",
		want: "fb0",
	}, {
		// This is the shape a firmware change would take: the mapping is
		// there but the layout after it is not what we calibrated against.
		name: "no anonymous region after fb0",
		maps: "b6d00000-b6d40000 rw-s 00000000 00:0d 5678 /dev/fb0\n" +
			"b6e00000-b6f00000 r-xp 00000000 b3:03 9 /usr/lib/libQt5Gui.so\n",
		want: "anonymous",
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseMaps(strings.NewReader(tc.maps))
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestToGray(t *testing.T) {
	// The frame is u16 with 0=black and 30=white, not rgb565 (M0 finding).
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
		if got := toGray(tc.raw); got != tc.want {
			t.Errorf("toGray(%d) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestRawIndexIsWithinTheFrame(t *testing.T) {
	for _, p := range [][2]int{{0, 0}, {ScreenW - 1, 0}, {0, ScreenH - 1}, {ScreenW - 1, ScreenH - 1}} {
		i := rawIndex(p[0], p[1])
		if i < 0 || i >= RawW*RawH {
			t.Errorf("rawIndex%v = %d, outside the %d-sample frame", p, i, RawW*RawH)
		}
	}
}

func TestRawIndexIsABijection(t *testing.T) {
	// Every screen pixel must map to a distinct raw sample; an overlap would
	// mean the rotation is dropping or duplicating rows.
	seen := make(map[int64]bool, ScreenW*ScreenH)
	for y := 0; y < ScreenH; y++ {
		for x := 0; x < ScreenW; x++ {
			i := rawIndex(x, y)
			if seen[i] {
				t.Fatalf("raw sample %d claimed twice, at screen (%d,%d)", i, x, y)
			}
			seen[i] = true
		}
	}
	if len(seen) != RawW*RawH {
		t.Errorf("covered %d of %d raw samples", len(seen), RawW*RawH)
	}
}

// TestRotateOrientation pins the portrait mapping: raw is landscape, and the
// view the user sees is a 90-degree clockwise turn plus a vertical flip
// (ffmpeg transpose=3, as calibrated in M0). Getting this wrong yields a
// screenshot that is mirrored or sideways.
func TestRotateOrientation(t *testing.T) {
	raw := make([]uint16, RawW*RawH)
	for i := range raw {
		raw[i] = grayMax // start with a blank white page
	}
	// Mark one raw sample black and check where it lands on screen.
	mark := func(rx, ry int) { raw[ry*RawW+rx] = 0 }

	tests := []struct {
		name         string
		rawX, rawY   int
		wantX, wantY int
	}{
		{"raw bottom-right maps to screen top-left", RawW - 1, RawH - 1, 0, 0},
		{"raw bottom-left maps to screen top-right", 0, RawH - 1, 0, ScreenH - 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for i := range raw {
				raw[i] = grayMax
			}
			mark(tc.rawX, tc.rawY)
			img := Rotate(raw)
			if got := img.GrayAt(tc.wantX, tc.wantY).Y; got != 0 {
				t.Errorf("screen (%d,%d) = %d, want the marked 0", tc.wantX, tc.wantY, got)
			}
			if n := CountInk(img); n != 1 {
				t.Errorf("one marked sample produced %d ink pixels", n)
			}
		})
	}
}

func TestRotateDimensions(t *testing.T) {
	img := Rotate(make([]uint16, RawW*RawH))
	if got := img.Bounds().Size(); got.X != ScreenW || got.Y != ScreenH {
		t.Errorf("rotated image is %v, want the %dx%d portrait screen", got, ScreenW, ScreenH)
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
