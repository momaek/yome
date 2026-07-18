// CJK stroke rendering: per-character centerline data from makemeahanzi
// (single-line strokes with stroke order — exactly the form pen injection
// wants), smoothed with Catmull-Rom so the sparse median points do not read
// as angular chicken scratch.
package layout

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"github.com/momaek/yome/assets"
	"github.com/momaek/yome/internal/geom"
)

// cjkEm is the glyph-space box size of the CJK data: characters are designed
// on a 1024x1024 em square, y down, baseline at the box bottom.
const cjkEm = 1024

// CJK is the parsed Chinese stroke table.
type CJK struct {
	glyphs map[rune][]geom.Stroke
}

var (
	cjkOnce sync.Once
	cjk     *CJK
	cjkErr  error
)

// Han returns the embedded CJK stroke table, parsing it on first use (the
// table is ~10k characters; parsing is a single pass but not free, so it is
// lazy — English-only sessions never pay for it).
func Han() (*CJK, error) {
	cjkOnce.Do(func() {
		cjk, cjkErr = ParseCJK(assets.CJKMedians)
	})
	return cjk, cjkErr
}

// ParseCJK reads the gzipped binary table written by tools/gen-cjk.
func ParseCJK(gz []byte) (*CJK, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("cjk data: %w", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("cjk data: %w", err)
	}
	if len(raw) < 8 || string(raw[:4]) != "CJK1" {
		return nil, fmt.Errorf("cjk data: bad magic")
	}
	n := binary.LittleEndian.Uint32(raw[4:8])
	c := &CJK{glyphs: make(map[rune][]geom.Stroke, n)}
	off := 8
	need := func(k int) error {
		if off+k > len(raw) {
			return fmt.Errorf("cjk data: truncated at offset %d", off)
		}
		return nil
	}
	for i := uint32(0); i < n; i++ {
		if err := need(5); err != nil {
			return nil, err
		}
		r := rune(binary.LittleEndian.Uint32(raw[off:]))
		strokeN := int(raw[off+4])
		off += 5
		strokes := make([]geom.Stroke, 0, strokeN)
		for s := 0; s < strokeN; s++ {
			if err := need(1); err != nil {
				return nil, err
			}
			ptN := int(raw[off])
			off++
			if err := need(ptN * 4); err != nil {
				return nil, err
			}
			pts := make([]geom.Point, ptN)
			for p := 0; p < ptN; p++ {
				pts[p] = geom.Point{
					X: float64(binary.LittleEndian.Uint16(raw[off:])),
					Y: float64(binary.LittleEndian.Uint16(raw[off+2:])),
				}
				off += 4
			}
			strokes = append(strokes, geom.Stroke{Points: pts})
		}
		c.glyphs[r] = strokes
	}
	return c, nil
}

// Len is the number of characters in the table.
func (c *CJK) Len() int { return len(c.glyphs) }

// Has reports whether the table covers r.
func (c *CJK) Has(r rune) bool {
	_, ok := c.glyphs[r]
	return ok
}

// Render emits r's strokes scaled to an em box of emPx with its top-left at
// origin. ok is false for characters outside the table.
func (c *CJK) Render(r rune, origin geom.Point, emPx float64) (strokes []geom.Stroke, ok bool) {
	src, ok := c.glyphs[r]
	if !ok {
		return nil, false
	}
	scale := emPx / cjkEm
	for _, s := range src {
		sm := smooth(s.Points)
		pts := make([]geom.Point, len(sm))
		for i, p := range sm {
			pts[i] = geom.Point{X: origin.X + p.X*scale, Y: origin.Y + p.Y*scale}
		}
		strokes = append(strokes, geom.Stroke{Points: pts})
	}
	return strokes, true
}

// smoothSubdiv is how many segments each median span becomes under the
// Catmull-Rom pass.
const smoothSubdiv = 6

// smooth interpolates a Catmull-Rom spline through the median points. The
// medians carry few points per stroke (often 3-6), so drawing them as straight
// segments would leave visible corners on hooks and curves; the spline passes
// exactly through every original point.
func smooth(pts []geom.Point) []geom.Point {
	if len(pts) < 3 {
		return pts
	}
	out := make([]geom.Point, 0, (len(pts)-1)*smoothSubdiv+1)
	out = append(out, pts[0])
	for i := 0; i < len(pts)-1; i++ {
		p1, p2 := pts[i], pts[i+1]
		p0 := pts[max(i-1, 0)]
		p3 := pts[min(i+2, len(pts)-1)]
		for j := 1; j <= smoothSubdiv; j++ {
			t := float64(j) / smoothSubdiv
			out = append(out, catmullRom(p0, p1, p2, p3, t))
		}
	}
	return out
}

// catmullRom evaluates the centripetal-free (uniform) Catmull-Rom basis.
func catmullRom(p0, p1, p2, p3 geom.Point, t float64) geom.Point {
	t2, t3 := t*t, t*t*t
	f := func(a0, a1, a2, a3 float64) float64 {
		return 0.5 * ((2 * a1) + (-a0+a2)*t + (2*a0-5*a1+4*a2-a3)*t2 + (-a0+3*a1-3*a2+a3)*t3)
	}
	return geom.Point{X: f(p0.X, p1.X, p2.X, p3.X), Y: f(p0.Y, p1.Y, p2.Y, p3.Y)}
}
