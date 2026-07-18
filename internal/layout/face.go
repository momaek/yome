package layout

import (
	"log/slog"
	"sync"

	"github.com/momaek/yome/internal/geom"
)

// cjkBaseline is where the baseline sits inside the CJK em box (source
// baseline y=0 maps to y'=900 of the 1024 box; the remainder is descender
// room).
const cjkBaseline = 900.0 / cjkEm

// DefaultHanScale is the CJK em height in cap heights. Han characters set
// solid squares, so they read best a bit larger than the Latin capitals they
// sit beside.
const DefaultHanScale = 1.4

// Face is the complete text-rendering surface: the Hershey single-stroke font
// for Latin, plus the Han stroke table for Chinese. Han may be nil, leaving a
// Latin-only face.
type Face struct {
	Latin    *Font
	Han      *CJK
	HanScale float64 // Han em height in cap heights
}

// NewFace combines a Latin font and an optional Han table.
func NewFace(latin *Font, han *CJK) *Face {
	return &Face{Latin: latin, Han: han, HanScale: DefaultHanScale}
}

var (
	defaultFaceOnce sync.Once
	defaultFace     *Face
)

// DefaultFace is the embedded Futural font plus the embedded Han table. A
// corrupt Han asset degrades to Latin-only with a warning rather than
// failing: English writing should not depend on the Chinese data.
func DefaultFace() *Face {
	defaultFaceOnce.Do(func() {
		han, err := Han()
		if err != nil {
			slog.Warn("CJK stroke data unavailable; Chinese output disabled", "err", err)
			han = nil
		}
		defaultFace = NewFace(Futural(), han)
	})
	return defaultFace
}

// hanScale returns the face's Han em multiplier.
func (f *Face) hanScale() float64 {
	if f.HanScale > 0 {
		return f.HanScale
	}
	return DefaultHanScale
}

// isHan reports whether r renders from the Han table.
func (f *Face) isHan(r rune) bool { return f.Han != nil && f.Han.Has(r) }

// fullwidthFallback maps CJK punctuation the Han table does not carry onto
// ASCII the Hershey font can draw. Losing the exact glyph beats dropping the
// mark entirely.
var fullwidthFallback = map[rune]rune{
	'，': ',', '。': '.', '！': '!', '？': '?', '：': ':', '；': ';',
	'、': ',', '（': '(', '）': ')', '《': '"', '》': '"',
	'「': '\'', '」': '\'',
	'“': '"', '”': '"', // curly double quotes
	'‘': '\'', '’': '\'', // curly single quotes
	'－': '-', '～': '~', '．': '.', '…': '.',
}

// resolve picks the drawable rune for r: Han stays put; unknown fullwidth
// punctuation degrades to its ASCII cousin for the Latin font.
func (f *Face) resolve(r rune) rune {
	if f.isHan(r) {
		return r
	}
	if sub, ok := fullwidthFallback[r]; ok {
		return sub
	}
	return r
}

// Advance is the horizontal step for r at the given cap height.
func (f *Face) Advance(r rune, capHeightPx float64) float64 {
	if f.isHan(r) {
		return f.hanScale() * capHeightPx
	}
	return f.Latin.Advance(f.resolve(r), capHeightPx)
}

// Measure is the total advance of s.
func (f *Face) Measure(s string, capHeightPx float64) float64 {
	var w float64
	for _, r := range s {
		w += f.Advance(r, capHeightPx)
	}
	return w
}

// hanBoxTop is the em-box top's offset from the cap top: the box is placed so
// its internal baseline coincides with the Latin baseline.
func (f *Face) hanBoxTop(capHeightPx float64) float64 {
	return capHeightPx - cjkBaseline*f.hanScale()*capHeightPx
}

// hanRisePx is how far Han glyphs may paint above the Latin cap line.
func (f *Face) hanRisePx(capHeightPx float64) float64 {
	if rise := -f.hanBoxTop(capHeightPx); rise > 0 {
		return rise
	}
	return 0
}

// hanDropPx is how far the Han em box reaches below the baseline.
func (f *Face) hanDropPx(capHeightPx float64) float64 {
	return (1 - cjkBaseline) * f.hanScale() * capHeightPx
}

// OvershootPx is the clearance a line needs above its cap top. han selects
// whether Han metrics participate (pure-Latin text should not pay Han
// headroom).
func (f *Face) OvershootPx(capHeightPx float64, han bool) float64 {
	o := f.Latin.OvershootPx(capHeightPx)
	if han {
		o = max(o, f.hanRisePx(capHeightPx))
	}
	return o
}

// DescentPx is the clearance a line needs below its baseline.
func (f *Face) DescentPx(capHeightPx float64, han bool) float64 {
	d := f.Latin.DescentPx(capHeightPx)
	if han {
		d = max(d, f.hanDropPx(capHeightPx))
	}
	return d
}

// RenderRune emits r with the line's cap top-left at origin, returning the
// strokes and the advance to the next glyph.
func (f *Face) RenderRune(r rune, origin geom.Point, capHeightPx float64) ([]geom.Stroke, float64) {
	if f.isHan(r) {
		em := f.hanScale() * capHeightPx
		strokes, _ := f.Han.Render(r, geom.Point{X: origin.X, Y: origin.Y + f.hanBoxTop(capHeightPx)}, em)
		return strokes, em
	}
	return f.Latin.RenderRune(f.resolve(r), origin, capHeightPx)
}

// containsHan reports whether any rune of s renders from the Han table.
func (f *Face) containsHan(s string) bool {
	for _, r := range s {
		if f.isHan(r) {
			return true
		}
	}
	return false
}
