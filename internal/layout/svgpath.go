package layout

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/momaek/yome/internal/geom"
)

// Subdivision counts for flattening beziers. Fixed rather than adaptive: at the
// stroke widths xochitl renders, the faceting is invisible on e-ink (verified
// on device in M0), and a fixed count keeps output deterministic for tests.
const (
	cubicSegments = 12
	quadSegments  = 10
)

var (
	pathTokenRe = regexp.MustCompile(`[MmLlHhVvCcSsQqTtAaZz]|[-+]?[0-9]*\.?[0-9]+(?:[eE][-+]?[0-9]+)?`)
	dAttrRe     = regexp.MustCompile(`\bd="([^"]+)"`)
)

// ParsePathD flattens one SVG path d-string into polylines in path space.
//
// Supports M L H V C S Q T Z in both absolute and relative form. Elliptical
// arcs (A/a) degrade to a straight line to the endpoint: models rarely emit
// them, and a chord is a safe approximation for handwriting-scale output.
func ParsePathD(d string) ([]geom.Stroke, error) {
	toks := pathTokenRe.FindAllString(d, -1)
	if len(toks) == 0 {
		return nil, fmt.Errorf("no path tokens in %q", d)
	}

	var (
		out            []geom.Stroke
		cur            []geom.Point
		cx, cy         float64 // current point
		sx, sy         float64 // subpath start, for Z
		rcx, rcy       float64 // last bezier control, for S/T reflection
		lastCmd        byte
		i              int
		numberOverflow bool
	)
	num := func() float64 {
		if i >= len(toks) {
			numberOverflow = true
			return 0
		}
		v, err := strconv.ParseFloat(toks[i], 64)
		if err != nil { // a command where a number was expected
			numberOverflow = true
			return 0
		}
		i++
		return v
	}
	flush := func() {
		if len(cur) > 1 {
			out = append(out, geom.Stroke{Points: cur})
		}
		cur = nil
	}
	lineTo := func(x, y float64) {
		cur = append(cur, geom.Point{X: x, Y: y})
		cx, cy = x, y
	}
	cubicTo := func(x1, y1, x2, y2, x, y float64) {
		x0, y0 := cx, cy
		for j := 1; j <= cubicSegments; j++ {
			t := float64(j) / cubicSegments
			u := 1 - t
			cur = append(cur, geom.Point{
				X: u*u*u*x0 + 3*u*u*t*x1 + 3*u*t*t*x2 + t*t*t*x,
				Y: u*u*u*y0 + 3*u*u*t*y1 + 3*u*t*t*y2 + t*t*t*y,
			})
		}
		cx, cy, rcx, rcy = x, y, x2, y2
	}
	quadTo := func(x1, y1, x, y float64) {
		x0, y0 := cx, cy
		for j := 1; j <= quadSegments; j++ {
			t := float64(j) / quadSegments
			u := 1 - t
			cur = append(cur, geom.Point{
				X: u*u*x0 + 2*u*t*x1 + t*t*x,
				Y: u*u*y0 + 2*u*t*y1 + t*t*y,
			})
		}
		cx, cy, rcx, rcy = x, y, x1, y1
	}

	for i < len(toks) && !numberOverflow {
		var cmd byte
		if c := toks[i][0]; len(toks[i]) == 1 && (c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			cmd = c
			i++
		} else {
			switch lastCmd { // implicit repeat; after a moveto it means lineto
			case 'M':
				cmd = 'L'
			case 'm':
				cmd = 'l'
			case 0:
				return nil, fmt.Errorf("path starts with a number, not a command: %q", d)
			default:
				cmd = lastCmd
			}
		}

		switch cmd {
		case 'M', 'm':
			flush()
			x, y := num(), num()
			if cmd == 'm' {
				x, y = cx+x, cy+y
			}
			cur = []geom.Point{{X: x, Y: y}}
			cx, cy, sx, sy = x, y, x, y
		case 'L', 'l':
			x, y := num(), num()
			if cmd == 'l' {
				x, y = cx+x, cy+y
			}
			lineTo(x, y)
		case 'H', 'h':
			x := num()
			if cmd == 'h' {
				x += cx
			}
			lineTo(x, cy)
		case 'V', 'v':
			y := num()
			if cmd == 'v' {
				y += cy
			}
			lineTo(cx, y)
		case 'C', 'c':
			x1, y1, x2, y2, x, y := num(), num(), num(), num(), num(), num()
			if cmd == 'c' {
				x1, y1, x2, y2, x, y = cx+x1, cy+y1, cx+x2, cy+y2, cx+x, cy+y
			}
			cubicTo(x1, y1, x2, y2, x, y)
		case 'S', 's':
			x2, y2, x, y := num(), num(), num(), num()
			if cmd == 's' {
				x2, y2, x, y = cx+x2, cy+y2, cx+x, cy+y
			}
			x1, y1 := cx, cy
			if lastCmd == 'C' || lastCmd == 'c' || lastCmd == 'S' || lastCmd == 's' {
				x1, y1 = 2*cx-rcx, 2*cy-rcy
			}
			cubicTo(x1, y1, x2, y2, x, y)
		case 'Q', 'q':
			x1, y1, x, y := num(), num(), num(), num()
			if cmd == 'q' {
				x1, y1, x, y = cx+x1, cy+y1, cx+x, cy+y
			}
			quadTo(x1, y1, x, y)
		case 'T', 't':
			x, y := num(), num()
			if cmd == 't' {
				x, y = cx+x, cy+y
			}
			x1, y1 := cx, cy
			if lastCmd == 'Q' || lastCmd == 'q' || lastCmd == 'T' || lastCmd == 't' {
				x1, y1 = 2*cx-rcx, 2*cy-rcy
			}
			quadTo(x1, y1, x, y)
		case 'A', 'a':
			num() // rx
			num() // ry
			num() // x-axis-rotation
			num() // large-arc-flag
			num() // sweep-flag
			x, y := num(), num()
			if cmd == 'a' {
				x, y = cx+x, cy+y
			}
			lineTo(x, y)
		case 'Z', 'z':
			lineTo(sx, sy)
			flush()
		default:
			return nil, fmt.Errorf("unsupported path command %q in %q", cmd, d)
		}
		lastCmd = cmd
	}
	if numberOverflow {
		return nil, fmt.Errorf("path ended mid-command (missing coordinates): %q", d)
	}
	flush()
	return out, nil
}

// ParsePaths flattens several d-strings into one stroke set in path space.
func ParsePaths(ds []string) ([]geom.Stroke, error) {
	var out []geom.Stroke
	for i, d := range ds {
		s, err := ParsePathD(d)
		if err != nil {
			return nil, fmt.Errorf("path %d: %w", i, err)
		}
		out = append(out, s...)
	}
	return out, nil
}

// ExtractSVGPaths pulls every path d-attribute out of an SVG document. It is a
// developer convenience for the draw-svg CLI command; the agent's draw tool
// passes d-strings straight to ParsePaths.
func ExtractSVGPaths(svg string) []string {
	var ds []string
	for _, m := range dAttrRe.FindAllStringSubmatch(svg, -1) {
		ds = append(ds, m[1])
	}
	return ds
}
