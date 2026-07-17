// SVG path parsing + flattening — prototype of the draw-tool pipeline:
// path d-strings -> polylines -> pen injection.
package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"
)

var pathTokRe = regexp.MustCompile(`[MmLlHhVvCcSsQqTtAaZz]|[-+]?[0-9]*\.?[0-9]+(?:[eE][-+]?[0-9]+)?`)

// parsePathD converts one SVG path d-string into polylines.
// Supports M L H V C S Q T Z (abs+rel); arcs degrade to straight lines.
func parsePathD(d string) [][]fpt {
	toks := pathTokRe.FindAllString(d, -1)
	var polys [][]fpt
	var cur []fpt
	var cx, cy, sx, sy, rcx, rcy float64 // current, subpath start, last control
	var lastCmd byte

	i := 0
	num := func() float64 {
		v, _ := strconv.ParseFloat(toks[i], 64)
		i++
		return v
	}
	flush := func() {
		if len(cur) > 1 {
			polys = append(polys, cur)
		}
		cur = nil
	}
	lineTo := func(x, y float64) {
		cur = append(cur, fpt{x, y})
		cx, cy = x, y
	}
	cubicTo := func(x1, y1, x2, y2, x, y float64) {
		x0, y0 := cx, cy
		for j := 1; j <= 12; j++ {
			t := float64(j) / 12
			u := 1 - t
			cur = append(cur, fpt{
				u*u*u*x0 + 3*u*u*t*x1 + 3*u*t*t*x2 + t*t*t*x,
				u*u*u*y0 + 3*u*u*t*y1 + 3*u*t*t*y2 + t*t*t*y,
			})
		}
		cx, cy, rcx, rcy = x, y, x2, y2
	}
	quadTo := func(x1, y1, x, y float64) {
		x0, y0 := cx, cy
		for j := 1; j <= 10; j++ {
			t := float64(j) / 10
			u := 1 - t
			cur = append(cur, fpt{u*u*x0 + 2*u*t*x1 + t*t*x, u*u*y0 + 2*u*t*y1 + t*t*y})
		}
		cx, cy, rcx, rcy = x, y, x1, y1
	}

	for i < len(toks) {
		var cmd byte
		if c := toks[i][0]; (c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') && len(toks[i]) == 1 {
			cmd = c
			i++
		} else { // implicit repeat; after M/m implicit L/l
			switch lastCmd {
			case 'M':
				cmd = 'L'
			case 'm':
				cmd = 'l'
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
			cur = []fpt{{x, y}}
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
			num() // rotation
			num() // large-arc
			num() // sweep
			x, y := num(), num()
			if cmd == 'a' {
				x, y = cx+x, cy+y
			}
			lineTo(x, y)
		case 'Z', 'z':
			lineTo(sx, sy)
			flush()
		}
		lastCmd = cmd
	}
	flush()
	return polys
}

var dAttrRe = regexp.MustCompile(`\bd="([^"]+)"`)

// svgCmd extracts all path d-attributes from an SVG file, scales the whole
// drawing to widthPx wide with its top-left at screen (x0,y0), and injects.
func svgCmd(dev, file string, x0, y0, widthPx, delayMs, pressure int) {
	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var polys [][]fpt
	for _, m := range dAttrRe.FindAllStringSubmatch(string(data), -1) {
		polys = append(polys, parsePathD(m[1])...)
	}
	if len(polys) == 0 {
		fmt.Fprintln(os.Stderr, "no paths found in", file)
		os.Exit(1)
	}

	minX, minY := polys[0][0].X, polys[0][0].Y
	maxX := minX
	for _, p := range polys {
		for _, q := range p {
			if q.X < minX {
				minX = q.X
			}
			if q.X > maxX {
				maxX = q.X
			}
			if q.Y < minY {
				minY = q.Y
			}
		}
	}
	scale := float64(widthPx) / (maxX - minX)

	f, err := os.OpenFile(dev, os.O_WRONLY, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	emit(f, evKey, btnToolPen, 1)
	report(f)
	time.Sleep(50 * time.Millisecond)
	for _, poly := range polys {
		pts := make([]fpt, len(poly))
		for i, p := range poly {
			pts[i] = fpt{float64(x0) + (p.X-minX)*scale, float64(y0) + (p.Y-minY)*scale}
		}
		drawStroke(f, pts, delayMs, pressure)
	}
	emit(f, evKey, btnToolPen, 0)
	report(f)
	fmt.Printf("svg done: %s (%d strokes) at (%d,%d) width=%dpx\n", file, len(polys), x0, y0, widthPx)
}
