package layout

import (
	"strings"

	"github.com/momaek/yome/internal/geom"
)

// MinLineSpacing is the floor on line spacing, in cap heights. Below this,
// descenders of one line collide with the caps of the next (M0 finding).
const MinLineSpacing = 1.8

// TextOptions controls typesetting. Area is the writable box in screen space,
// already inset by the page margins.
type TextOptions struct {
	CapHeightPx float64 // capital letter height
	LineSpacing float64 // multiple of CapHeightPx; clamped up to MinLineSpacing
	Area        geom.Rect
}

// TextResult is the outcome of typesetting a block of text.
type TextResult struct {
	Strokes  []geom.Stroke // ready to inject
	Lines    []string      // the lines that fit, as laid out
	Overflow []string      // wrapped lines that did not fit in Area
}

// Text wraps text at word boundaries and renders the lines that fit inside
// opt.Area. Lines past the bottom of the area are not rendered; they come back
// in Overflow for the caller to continue on another page.
//
// Newlines in text are honoured as hard line breaks. A word longer than the
// area is broken mid-word rather than dropped.
func Text(f *Font, text string, opt TextOptions) TextResult {
	if opt.LineSpacing < MinLineSpacing {
		opt.LineSpacing = MinLineSpacing
	}
	wrapped := Wrap(f, text, opt.CapHeightPx, opt.Area.W)

	lineH := opt.CapHeightPx * opt.LineSpacing
	descent := f.DescentPx(opt.CapHeightPx)
	// Ascenders paint above the cap line, so the first line's cap top starts
	// that much below the area's edge; otherwise 'd' and 'l' cross the margin.
	top := opt.Area.Y + f.OvershootPx(opt.CapHeightPx)

	var res TextResult
	for i, line := range wrapped {
		y := top + float64(i)*lineH
		// The line must clear the bottom edge including its descenders.
		if y+opt.CapHeightPx+descent > opt.Area.Bottom() {
			res.Overflow = wrapped[i:]
			break
		}
		res.Lines = append(res.Lines, line)
		penX := opt.Area.X
		for _, r := range line {
			strokes, adv := f.RenderRune(r, geom.Point{X: penX, Y: y}, opt.CapHeightPx)
			res.Strokes = append(res.Strokes, strokes...)
			penX += adv
		}
	}
	return res
}

// Wrap breaks text into lines no wider than widthPx, splitting at spaces.
func Wrap(f *Font, text string, capHeightPx, widthPx float64) []string {
	var lines []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "") // preserve blank lines between paragraphs
			continue
		}
		spaceW := f.Advance(' ', capHeightPx)
		var cur string
		var curW float64
		for _, w := range words {
			for _, piece := range breakLongWord(f, w, capHeightPx, widthPx) {
				pieceW := f.Measure(piece, capHeightPx)
				switch {
				case cur == "":
					cur, curW = piece, pieceW
				case curW+spaceW+pieceW <= widthPx:
					cur, curW = cur+" "+piece, curW+spaceW+pieceW
				default:
					lines = append(lines, cur)
					cur, curW = piece, pieceW
				}
			}
		}
		lines = append(lines, cur)
	}
	return lines
}

// breakLongWord splits a word that cannot fit on a line by itself. Words that
// fit come back unchanged as a single piece.
func breakLongWord(f *Font, word string, capHeightPx, widthPx float64) []string {
	if f.Measure(word, capHeightPx) <= widthPx {
		return []string{word}
	}
	var pieces []string
	var cur strings.Builder
	var curW float64
	for _, r := range word {
		w := f.Advance(r, capHeightPx)
		if cur.Len() > 0 && curW+w > widthPx {
			pieces = append(pieces, cur.String())
			cur.Reset()
			curW = 0
		}
		cur.WriteRune(r)
		curW += w
	}
	if cur.Len() > 0 {
		pieces = append(pieces, cur.String())
	}
	return pieces
}

// PageBudget is how many characters of typical prose fit in area. It feeds the
// per-page budget the system prompt hands the model, so it is an estimate:
// based on the average advance of the printable ASCII range.
func PageBudget(f *Font, opt TextOptions) int {
	if opt.LineSpacing < MinLineSpacing {
		opt.LineSpacing = MinLineSpacing
	}
	var total float64
	var n int
	for r := ' '; r <= '~'; r++ {
		if _, ok := f.Glyph(r); ok {
			total += f.Advance(r, opt.CapHeightPx)
			n++
		}
	}
	if n == 0 || total == 0 {
		return 0
	}
	avg := total / float64(n)
	perLine := int(opt.Area.W / avg)
	lineH := opt.CapHeightPx * opt.LineSpacing
	descent := f.DescentPx(opt.CapHeightPx)
	usable := opt.Area.H - f.OvershootPx(opt.CapHeightPx) - opt.CapHeightPx - descent
	if usable < 0 {
		return 0
	}
	lines := int(usable/lineH) + 1
	return perLine * lines
}

// FitOptions controls how a drawing is scaled into the page.
type FitOptions struct {
	Area  geom.Rect
	Scale float64 // fraction of Area to occupy; <=0 or >1 means fill
}

// Fit normalises strokes in arbitrary path space to sit inside opt.Area,
// preserving aspect ratio and anchoring at the area's top-left, then clips
// anything still outside. Empty input returns nil.
func Fit(strokes []geom.Stroke, opt FitOptions) []geom.Stroke {
	b, ok := geom.Bounds(strokes)
	if !ok {
		return nil
	}
	frac := opt.Scale
	if frac <= 0 || frac > 1 {
		frac = 1
	}

	scale := 1.0
	if b.W > 0 && b.H > 0 {
		scale = min(opt.Area.W/b.W, opt.Area.H/b.H) * frac
	} else if b.W > 0 {
		scale = opt.Area.W / b.W * frac
	} else if b.H > 0 {
		scale = opt.Area.H / b.H * frac
	}

	out := geom.Transform(strokes, scale, opt.Area.X-b.X*scale, opt.Area.Y-b.Y*scale)
	return geom.Clip(out, opt.Area)
}
