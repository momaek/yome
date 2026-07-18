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

// Text wraps text and renders the lines that fit inside opt.Area. Latin text
// breaks at word boundaries; Han characters may break anywhere, so mixed and
// pure-Chinese text wraps naturally. Lines past the bottom of the area are
// not rendered; they come back in Overflow for the caller to continue on
// another page.
//
// Newlines in text are honoured as hard line breaks. A word longer than the
// area is broken mid-word rather than dropped.
func Text(face *Face, text string, opt TextOptions) TextResult {
	if opt.LineSpacing < MinLineSpacing {
		opt.LineSpacing = MinLineSpacing
	}
	wrapped := Wrap(face, text, opt.CapHeightPx, opt.Area.W)

	// Pure-Latin text should not pay Han headroom, so the metrics only
	// include the Han box when the text actually uses it.
	han := face.containsHan(text)
	lineH := opt.CapHeightPx * opt.LineSpacing
	descent := face.DescentPx(opt.CapHeightPx, han)
	// Ascenders (and taller Han boxes) paint above the cap line, so the first
	// line starts low enough that nothing crosses the margin.
	top := opt.Area.Y + face.OvershootPx(opt.CapHeightPx, han)

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
			strokes, adv := face.RenderRune(r, geom.Point{X: penX, Y: y}, opt.CapHeightPx)
			res.Strokes = append(res.Strokes, strokes...)
			penX += adv
		}
	}
	return res
}

// unit is one unbreakable run of text: a Latin word, or a single Han
// character. space records whether a breakable space preceded it in the
// source (dropped when the unit starts a line).
type unit struct {
	text  string
	space bool
}

// tokenize splits a paragraph into wrap units. Han characters become units of
// their own — Chinese has no inter-word spaces, so every character boundary
// is a legal break point.
func tokenize(face *Face, para string) []unit {
	var units []unit
	var word strings.Builder
	pendingSpace := false
	wordSpace := false

	flush := func() {
		if word.Len() > 0 {
			units = append(units, unit{text: word.String(), space: wordSpace})
			word.Reset()
		}
	}
	for _, r := range para {
		switch {
		case r == ' ' || r == '\t':
			flush()
			pendingSpace = true
		case face.isHan(r):
			flush()
			units = append(units, unit{text: string(r), space: pendingSpace})
			pendingSpace = false
		default:
			if word.Len() == 0 {
				wordSpace = pendingSpace
				pendingSpace = false
			}
			word.WriteRune(r)
		}
	}
	flush()
	return units
}

// Wrap breaks text into lines no wider than widthPx.
func Wrap(face *Face, text string, capHeightPx, widthPx float64) []string {
	spaceW := face.Advance(' ', capHeightPx)
	var lines []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		units := tokenize(face, para)
		if len(units) == 0 {
			lines = append(lines, "") // preserve blank lines between paragraphs
			continue
		}
		var cur strings.Builder
		var curW float64
		push := func() {
			lines = append(lines, cur.String())
			cur.Reset()
			curW = 0
		}
		for _, u := range units {
			for pi, piece := range breakLongUnit(face, u.text, capHeightPx, widthPx) {
				pieceW := face.Measure(piece, capHeightPx)
				withSpace := u.space && pi == 0 && cur.Len() > 0
				joinW := pieceW
				if withSpace {
					joinW += spaceW
				}
				switch {
				case cur.Len() == 0:
					cur.WriteString(piece)
					curW = pieceW
				case curW+joinW <= widthPx:
					if withSpace {
						cur.WriteString(" ")
					}
					cur.WriteString(piece)
					curW += joinW
				default:
					push()
					cur.WriteString(piece)
					curW = pieceW
				}
			}
		}
		push()
	}
	return lines
}

// breakLongUnit splits a unit that cannot fit on a line by itself. Units that
// fit come back unchanged as a single piece.
func breakLongUnit(face *Face, text string, capHeightPx, widthPx float64) []string {
	if face.Measure(text, capHeightPx) <= widthPx {
		return []string{text}
	}
	var pieces []string
	var cur strings.Builder
	var curW float64
	for _, r := range text {
		w := face.Advance(r, capHeightPx)
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

// PageBudget is how many characters of typical Latin prose fit in the area.
// It feeds the per-page budget the system prompt hands the model, so it is an
// estimate: based on the average advance of the printable ASCII range.
func PageBudget(face *Face, opt TextOptions) int {
	if opt.LineSpacing < MinLineSpacing {
		opt.LineSpacing = MinLineSpacing
	}
	f := face.Latin
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
	return perLine * budgetLines(face, opt, false)
}

// HanPageBudget is how many Han characters fit in the area. Zero without a
// Han table.
func HanPageBudget(face *Face, opt TextOptions) int {
	if face.Han == nil {
		return 0
	}
	if opt.LineSpacing < MinLineSpacing {
		opt.LineSpacing = MinLineSpacing
	}
	perLine := int(opt.Area.W / (face.hanScale() * opt.CapHeightPx))
	return perLine * budgetLines(face, opt, true)
}

// budgetLines is how many lines fit vertically.
func budgetLines(face *Face, opt TextOptions, han bool) int {
	lineH := opt.CapHeightPx * opt.LineSpacing
	usable := opt.Area.H - face.OvershootPx(opt.CapHeightPx, han) - opt.CapHeightPx - face.DescentPx(opt.CapHeightPx, han)
	if usable < 0 {
		return 0
	}
	return int(usable/lineH) + 1
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
