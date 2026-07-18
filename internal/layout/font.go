// Package layout turns text and SVG paths into pen strokes.
//
// Everything here is pure geometry: no device access, no I/O beyond the
// embedded font data, so the whole package is testable off-device.
package layout

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/momaek/yome/assets"
	"github.com/momaek/yome/internal/geom"
)

// Glyph is one Hershey character in glyph space (y down, arbitrary units).
type Glyph struct {
	Left, Right float64 // horizontal margins; Right-Left is the advance
	Strokes     []geom.Stroke
}

// Font is a parsed Hershey single-stroke font.
type Font struct {
	glyphs map[rune]Glyph

	capTop  float64 // glyph-space y of the capital top, from 'H'
	capBase float64 // glyph-space y of the baseline, from 'H'
	minY    float64 // highest point of any glyph, for ascender clearance
	maxY    float64 // lowest point of any glyph, for descender clearance
}

var futural *Font

// Futural returns the embedded Hershey Simplex font, the default for Latin
// text.
func Futural() *Font {
	if futural == nil {
		futural = mustParseJHF("futural.jhf", assets.FuturalJHF)
	}
	return futural
}

// mustParseJHF panics on parse failure, which for embedded data would be a
// build-time defect.
func mustParseJHF(name, data string) *Font {
	f, err := ParseJHF(data)
	if err != nil {
		panic(fmt.Sprintf("layout: embedded %s is corrupt: %v", name, err))
	}
	return f
}

// ParseJHF reads Hershey JHF font data.
//
// Line format: cols 0-4 glyph id, cols 5-7 vertex count (including the
// left/right margin pair), then vertex pairs encoded as (char-'R'). The pair
// " R" is a pen-up. Long glyphs wrap across lines. Glyphs are assigned to
// runes sequentially starting at ' '.
func ParseJHF(data string) (*Font, error) {
	f := &Font{glyphs: make(map[rune]Glyph, 96)}
	lines := strings.Split(data, "\n")
	ch := ' '
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 8 {
			return nil, fmt.Errorf("line %d: too short for a JHF header: %q", i+1, line)
		}
		nv, err := strconv.Atoi(strings.TrimSpace(line[5:8]))
		if err != nil {
			return nil, fmt.Errorf("line %d: bad vertex count %q: %w", i+1, line[5:8], err)
		}
		// A glyph's vertex data may continue on the following lines.
		need := 8 + nv*2
		for len(line) < need && i+1 < len(lines) {
			i++
			line += strings.TrimRight(lines[i], "\r")
		}
		if len(line) < need {
			return nil, fmt.Errorf("glyph %q: want %d vertex chars, got %d", ch, nv*2, len(line)-8)
		}

		coords := line[8:need]
		g := Glyph{Left: float64(coords[0]) - 'R', Right: float64(coords[1]) - 'R'}
		var cur []geom.Point
		for j := 2; j+1 < nv*2; j += 2 {
			if coords[j] == ' ' && coords[j+1] == 'R' { // pen up
				if len(cur) > 1 {
					g.Strokes = append(g.Strokes, geom.Stroke{Points: cur})
				}
				cur = nil
				continue
			}
			cur = append(cur, geom.Point{X: float64(coords[j]) - 'R', Y: float64(coords[j+1]) - 'R'})
		}
		if len(cur) > 1 {
			g.Strokes = append(g.Strokes, geom.Stroke{Points: cur})
		}
		f.glyphs[ch] = g
		ch++
	}

	h, ok := f.glyphs['H']
	if !ok || len(h.Strokes) == 0 {
		return nil, fmt.Errorf("font has no 'H' glyph to calibrate cap height from")
	}
	b, _ := geom.Bounds(h.Strokes)
	f.capTop, f.capBase = b.Y, b.Bottom()
	if f.capBase <= f.capTop {
		return nil, fmt.Errorf("degenerate cap height from 'H': %v..%v", f.capTop, f.capBase)
	}
	f.minY, f.maxY = f.capTop, f.capBase
	for _, g := range f.glyphs {
		b, ok := geom.Bounds(g.Strokes)
		if !ok {
			continue
		}
		f.minY = math.Min(f.minY, b.Y)
		f.maxY = math.Max(f.maxY, b.Bottom())
	}
	return f, nil
}

// Glyph looks up a rune. ok is false for runes the font does not cover.
func (f *Font) Glyph(r rune) (Glyph, bool) {
	g, ok := f.glyphs[r]
	return g, ok
}

// scaleFor converts a cap height in pixels into a glyph-space scale factor.
func (f *Font) scaleFor(capHeightPx float64) float64 {
	return capHeightPx / (f.capBase - f.capTop)
}

// missingAdvance is the width given to runes outside the font, in cap heights.
const missingAdvance = 0.5

// Advance is the horizontal step for a rune at the given cap height.
func (f *Font) Advance(r rune, capHeightPx float64) float64 {
	g, ok := f.glyphs[r]
	if !ok {
		return missingAdvance * capHeightPx
	}
	return (g.Right - g.Left) * f.scaleFor(capHeightPx)
}

// Measure is the total advance of s at the given cap height.
func (f *Font) Measure(s string, capHeightPx float64) float64 {
	var w float64
	for _, r := range s {
		w += f.Advance(r, capHeightPx)
	}
	return w
}

// DescentPx is how far below the baseline the font's deepest glyph reaches.
func (f *Font) DescentPx(capHeightPx float64) float64 {
	return (f.maxY - f.capBase) * f.scaleFor(capHeightPx)
}

// OvershootPx is how far above the capital line the font's tallest glyph
// reaches. In Hershey Simplex the lowercase ascenders (b, d, f, h, k, l) clear
// the capitals slightly, so a line anchored at its cap top still paints a
// couple of pixels above it.
func (f *Font) OvershootPx(capHeightPx float64) float64 {
	return (f.capTop - f.minY) * f.scaleFor(capHeightPx)
}

// RenderRune emits the glyph's strokes with its cap top-left at origin.
// The returned advance is where the next glyph starts.
func (f *Font) RenderRune(r rune, origin geom.Point, capHeightPx float64) (strokes []geom.Stroke, advance float64) {
	g, ok := f.glyphs[r]
	if !ok {
		return nil, missingAdvance * capHeightPx
	}
	scale := f.scaleFor(capHeightPx)
	for _, s := range g.Strokes {
		pts := make([]geom.Point, len(s.Points))
		for i, p := range s.Points {
			pts[i] = geom.Point{
				X: origin.X + (p.X-g.Left)*scale,
				Y: origin.Y + (p.Y-f.capTop)*scale,
			}
		}
		strokes = append(strokes, geom.Stroke{Points: pts})
	}
	return strokes, (g.Right - g.Left) * scale
}
