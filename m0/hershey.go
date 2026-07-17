// Hershey futural (Simplex) single-stroke font: JHF parsing + text rendering.
//
// JHF line format: cols 0-4 glyph id, cols 5-7 vertex count (incl. the
// left/right margin pair), then vertex pairs encoded as (char-'R');
// the pair " R" means pen-up. Long glyphs wrap across lines.
package main

import (
	_ "embed"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

//go:embed futural.jhf
var futuralJHF string

type hGlyph struct {
	left, right float64
	strokes     [][]fpt
}

var (
	hershey  map[rune]hGlyph
	hCapTop  float64 // glyph-space y of capital top ('H' bbox)
	hCapBase float64 // glyph-space y of baseline ('H' bbox bottom)
)

func loadHershey() {
	hershey = make(map[rune]hGlyph, 96)
	lines := strings.Split(futuralJHF, "\n")
	ch := rune(' ')
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		nv, err := strconv.Atoi(strings.TrimSpace(line[5:8]))
		if err != nil {
			continue
		}
		need := 8 + nv*2
		for len(line) < need && i+1 < len(lines) {
			i++
			line += strings.TrimRight(lines[i], "\r")
		}
		coords := line[8:need]
		g := hGlyph{left: float64(coords[0]) - 'R', right: float64(coords[1]) - 'R'}
		var cur []fpt
		for j := 2; j+1 < nv*2; j += 2 {
			if coords[j] == ' ' && coords[j+1] == 'R' {
				if len(cur) > 1 {
					g.strokes = append(g.strokes, cur)
				}
				cur = nil
				continue
			}
			cur = append(cur, fpt{float64(coords[j]) - 'R', float64(coords[j+1]) - 'R'})
		}
		if len(cur) > 1 {
			g.strokes = append(g.strokes, cur)
		}
		hershey[ch] = g
		ch++
	}

	h := hershey['H']
	hCapTop, hCapBase = h.strokes[0][0].Y, h.strokes[0][0].Y
	for _, s := range h.strokes {
		for _, p := range s {
			if p.Y < hCapTop {
				hCapTop = p.Y
			}
			if p.Y > hCapBase {
				hCapBase = p.Y
			}
		}
	}
}

// textHershey writes text (supports "\n" via literal backslash-n) with the
// cap top-left at screen (x0,y0). sizePx is the capital height in pixels.
func textHershey(dev, text string, x0, y0, sizePx, delayMs, pressure int) {
	loadHershey()
	f, err := os.OpenFile(dev, os.O_WRONLY, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	scale := float64(sizePx) / (hCapBase - hCapTop)
	lineH := float64(sizePx) * 1.6
	text = strings.ReplaceAll(text, "\\n", "\n")

	emit(f, evKey, btnToolPen, 1)
	report(f)
	time.Sleep(50 * time.Millisecond)

	penX, penY := float64(x0), float64(y0)
	for _, ch := range text {
		if ch == '\n' {
			penX, penY = float64(x0), penY+lineH
			continue
		}
		g, ok := hershey[ch]
		if !ok {
			penX += 0.5 * float64(sizePx)
			continue
		}
		for _, stroke := range g.strokes {
			pts := make([]fpt, len(stroke))
			for i, p := range stroke {
				pts[i] = fpt{penX + (p.X-g.left)*scale, penY + (p.Y-hCapTop)*scale}
			}
			drawStroke(f, pts, delayMs, pressure)
		}
		penX += (g.right - g.left) * scale
	}

	emit(f, evKey, btnToolPen, 0)
	report(f)
	fmt.Printf("hershey text done: %q at (%d,%d) size=%dpx\n", text, x0, y0, sizePx)
}
