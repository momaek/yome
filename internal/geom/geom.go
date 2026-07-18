// Package geom holds the coordinate primitives shared by layout and inject.
//
// All coordinates are screen space: portrait 1404x1872, origin top-left,
// y growing downwards. Device-space conversion happens only in internal/inject.
package geom

import "math"

// Screen dimensions of the reMarkable 2 in portrait orientation.
const (
	ScreenW = 1404
	ScreenH = 1872
)

// Landscape view dimensions: the same panel turned 90°. The physical frame
// and both input coordinate systems stay portrait; only the rendered content
// rotates (calibrated 2026-07-18 on firmware 3.27.3.0).
const (
	LandscapeW = ScreenH
	LandscapeH = ScreenW
)

// Point is a screen-space position in pixels.
type Point struct{ X, Y float64 }

// LandscapeToPortrait maps a landscape-view point (1872x1404, as content is
// laid out when the notebook is in landscape) onto the physical portrait
// frame: phys_x = view_y, phys_y = 1871 - view_x. Injection and probing
// always happen in physical coordinates; this is the one place the view
// rotation is applied.
func LandscapeToPortrait(p Point) Point {
	return Point{X: p.Y, Y: float64(ScreenH-1) - p.X}
}

// PortraitToLandscape is the inverse of LandscapeToPortrait.
func PortraitToLandscape(p Point) Point {
	return Point{X: float64(ScreenH-1) - p.Y, Y: p.X}
}

// LandscapeStrokes maps strokes laid out in landscape-view coordinates onto
// the physical portrait frame for injection.
func LandscapeStrokes(strokes []Stroke) []Stroke {
	out := make([]Stroke, len(strokes))
	for i, s := range strokes {
		pts := make([]Point, len(s.Points))
		for j, p := range s.Points {
			pts[j] = LandscapeToPortrait(p)
		}
		out[i] = Stroke{Points: pts}
	}
	return out
}

// LandscapePage is the full canvas in landscape-view coordinates.
func LandscapePage() Rect { return Rect{0, 0, LandscapeW, LandscapeH} }

// Stroke is one pen-down..pen-up polyline. A stroke with fewer than two
// points is degenerate and is not injected.
type Stroke struct{ Points []Point }

// Length is the summed segment length of the stroke in pixels.
func (s Stroke) Length() float64 {
	var total float64
	for i := 1; i < len(s.Points); i++ {
		total += math.Hypot(s.Points[i].X-s.Points[i-1].X, s.Points[i].Y-s.Points[i-1].Y)
	}
	return total
}

// Rect is an axis-aligned screen-space rectangle.
type Rect struct{ X, Y, W, H float64 }

// FullPage is the whole canvas.
func FullPage() Rect { return Rect{0, 0, ScreenW, ScreenH} }

// Right returns the x coordinate of the rectangle's right edge.
func (r Rect) Right() float64 { return r.X + r.W }

// Bottom returns the y coordinate of the rectangle's bottom edge.
func (r Rect) Bottom() float64 { return r.Y + r.H }

// Contains reports whether p lies inside r (edges included).
func (r Rect) Contains(p Point) bool {
	return p.X >= r.X && p.X <= r.Right() && p.Y >= r.Y && p.Y <= r.Bottom()
}

// Inset shrinks r by the given margins.
func (r Rect) Inset(top, right, bottom, left float64) Rect {
	return Rect{X: r.X + left, Y: r.Y + top, W: r.W - left - right, H: r.H - top - bottom}
}

// Bounds returns the bounding box of a set of strokes. ok is false when the
// strokes hold no points at all.
func Bounds(strokes []Stroke) (b Rect, ok bool) {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, s := range strokes {
		for _, p := range s.Points {
			ok = true
			minX, minY = math.Min(minX, p.X), math.Min(minY, p.Y)
			maxX, maxY = math.Max(maxX, p.X), math.Max(maxY, p.Y)
		}
	}
	if !ok {
		return Rect{}, false
	}
	return Rect{X: minX, Y: minY, W: maxX - minX, H: maxY - minY}, true
}

// Transform applies a scale about the origin followed by a translation.
func Transform(strokes []Stroke, scale, dx, dy float64) []Stroke {
	out := make([]Stroke, len(strokes))
	for i, s := range strokes {
		pts := make([]Point, len(s.Points))
		for j, p := range s.Points {
			pts[j] = Point{p.X*scale + dx, p.Y*scale + dy}
		}
		out[i] = Stroke{Points: pts}
	}
	return out
}

// Clip cuts the strokes against r with Liang-Barsky, splitting a polyline into
// several strokes wherever it leaves and re-enters the rectangle.
func Clip(strokes []Stroke, r Rect) []Stroke {
	var out []Stroke
	for _, s := range strokes {
		var cur []Point
		flush := func() {
			if len(cur) > 1 {
				out = append(out, Stroke{Points: cur})
			}
			cur = nil
		}
		for i := 1; i < len(s.Points); i++ {
			a, b := s.Points[i-1], s.Points[i]
			ca, cb, visible := clipSegment(a, b, r)
			if !visible {
				flush()
				continue
			}
			// A clipped start means the polyline re-entered here, so the run
			// of points collected so far ends and a new stroke begins.
			if len(cur) == 0 || ca != a {
				flush()
				cur = append(cur, ca)
			}
			cur = append(cur, cb)
			if cb != b { // leaves the rect at cb
				flush()
			}
		}
		flush()
	}
	return out
}

// clipSegment trims segment a..b to r, reporting whether any of it survives.
func clipSegment(a, b Point, r Rect) (Point, Point, bool) {
	dx, dy := b.X-a.X, b.Y-a.Y
	t0, t1 := 0.0, 1.0
	for _, e := range [4]struct{ p, q float64 }{
		{-dx, a.X - r.X},
		{dx, r.Right() - a.X},
		{-dy, a.Y - r.Y},
		{dy, r.Bottom() - a.Y},
	} {
		switch {
		case e.p == 0:
			if e.q < 0 {
				return Point{}, Point{}, false // parallel and outside
			}
		case e.p < 0:
			if t := e.q / e.p; t > t1 {
				return Point{}, Point{}, false
			} else if t > t0 {
				t0 = t
			}
		default:
			if t := e.q / e.p; t < t0 {
				return Point{}, Point{}, false
			} else if t < t1 {
				t1 = t
			}
		}
	}
	return Point{a.X + t0*dx, a.Y + t0*dy}, Point{a.X + t1*dx, a.Y + t1*dy}, true
}
