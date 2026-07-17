package inject

import (
	"testing"

	"github.com/momaek/yome/internal/geom"
)

// The pen digitizer is mounted rotated relative to the portrait screen. These
// corner mappings are the M0 calibration; if they drift, every stroke lands in
// the wrong place, so they are pinned exactly.
func TestToWacomCorners(t *testing.T) {
	tests := []struct {
		name   string
		screen geom.Point
		x, y   int32
	}{
		{"top-left", geom.Point{X: 0, Y: 0}, WacomMaxX, 0},
		{"top-right", geom.Point{X: geom.ScreenW, Y: 0}, WacomMaxX, WacomMaxY},
		{"bottom-left", geom.Point{X: 0, Y: geom.ScreenH}, 0, 0},
		{"bottom-right", geom.Point{X: geom.ScreenW, Y: geom.ScreenH}, 0, WacomMaxY},
		{"centre", geom.Point{X: geom.ScreenW / 2, Y: geom.ScreenH / 2}, WacomMaxX / 2, WacomMaxY / 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			x, y := ToWacom(tc.screen)
			if x != tc.x || y != tc.y {
				t.Errorf("ToWacom(%+v) = (%d,%d), want (%d,%d)", tc.screen, x, y, tc.x, tc.y)
			}
		})
	}
}

func TestToWacomAxesAreIndependent(t *testing.T) {
	// Moving right on screen must move only the digitizer's y axis, and
	// moving down only its x axis. Swapping these is the classic transposition
	// bug and produces a mirrored page.
	x0, y0 := ToWacom(geom.Point{X: 100, Y: 100})
	x1, y1 := ToWacom(geom.Point{X: 500, Y: 100})
	if x1 != x0 {
		t.Errorf("moving right changed wacom x: %d -> %d", x0, x1)
	}
	if y1 <= y0 {
		t.Errorf("moving right did not increase wacom y: %d -> %d", y0, y1)
	}

	x2, y2 := ToWacom(geom.Point{X: 100, Y: 500})
	if y2 != y0 {
		t.Errorf("moving down changed wacom y: %d -> %d", y0, y2)
	}
	if x2 >= x0 {
		t.Errorf("moving down did not decrease wacom x: %d -> %d", x0, x2)
	}
}

func TestToWacomClampsOffScreenPoints(t *testing.T) {
	for _, p := range []geom.Point{{X: -500, Y: -500}, {X: 9999, Y: 9999}} {
		x, y := ToWacom(p)
		if x < 0 || x > WacomMaxX || y < 0 || y > WacomMaxY {
			t.Errorf("ToWacom(%+v) = (%d,%d), outside the digitizer's range", p, x, y)
		}
	}
}

// The touchscreen shares the screen's x axis but runs y the other way. This
// flip lives only here, which is why UI maps can stay in screen space.
func TestToTouchFlipsY(t *testing.T) {
	tests := []struct {
		name   string
		screen geom.Point
		x, y   int32
	}{
		{"top-left", geom.Point{X: 0, Y: 0}, 0, TouchMaxY},
		{"bottom-left", geom.Point{X: 0, Y: TouchMaxY}, 0, 0},
		{"toolbar toggle", geom.Point{X: 55, Y: 52}, 55, TouchMaxY - 52},
		{"erase all button", geom.Point{X: 290, Y: 794}, 290, TouchMaxY - 794},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			x, y := ToTouch(tc.screen)
			if x != tc.x || y != tc.y {
				t.Errorf("ToTouch(%+v) = (%d,%d), want (%d,%d)", tc.screen, x, y, tc.x, tc.y)
			}
		})
	}
}

func TestToTouchClamps(t *testing.T) {
	for _, p := range []geom.Point{{X: -100, Y: -100}, {X: 9999, Y: 9999}} {
		x, y := ToTouch(p)
		if x < 0 || x > TouchMaxX || y < 0 || y > TouchMaxY {
			t.Errorf("ToTouch(%+v) = (%d,%d), outside the touchscreen's range", p, x, y)
		}
	}
}
