// Package inject synthesises evdev events on the reMarkable 2's pen and touch
// input devices. xochitl cannot tell these from a real pen.
package inject

import "github.com/momaek/yome/internal/geom"

// Device coordinate ranges, measured on firmware 3.11.2.5 (M0).
const (
	WacomMaxX    = 20966 // along the screen's long edge
	WacomMaxY    = 15725 // along the screen's short edge
	WacomMaxPres = 4095

	TouchMaxX = 1403
	TouchMaxY = 1871
)

// ToWacom converts a screen-space point to pen digitizer coordinates.
//
// The digitizer is mounted rotated relative to the portrait screen:
//
//	wacom_x = (1 - screen_y/1872) * 20966
//	wacom_y = screen_x * 15725/1404
func ToWacom(p geom.Point) (x, y int32) {
	wx := (1 - p.Y/geom.ScreenH) * WacomMaxX
	wy := p.X * WacomMaxY / geom.ScreenW
	return int32(clamp(wx, 0, WacomMaxX)), int32(clamp(wy, 0, WacomMaxY))
}

// ToTouch converts a screen-space point to touchscreen coordinates. The
// touchscreen shares the screen's x axis but its y axis runs the other way.
// This is the single place that flip happens, so UI maps stay in screen space.
func ToTouch(p geom.Point) (x, y int32) {
	return int32(clamp(p.X, 0, TouchMaxX)), int32(clamp(TouchMaxY-p.Y, 0, TouchMaxY))
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
