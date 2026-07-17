package layout

import (
	"math"
	"testing"

	"github.com/momaek/yome/internal/geom"
)

func TestParseJHFEmbedded(t *testing.T) {
	f := Futural()

	// The Simplex font covers printable ASCII.
	for r := ' '; r <= '~'; r++ {
		if _, ok := f.Glyph(r); !ok {
			t.Errorf("no glyph for %q", r)
		}
	}
	if _, ok := f.Glyph('€'); ok {
		t.Error("font unexpectedly covers €")
	}
}

func TestParseJHFGlyphShape(t *testing.T) {
	f := Futural()

	// 'H' is three strokes: two uprights and the crossbar. It is also the
	// glyph the cap height is calibrated from, so a regression here silently
	// rescales all text.
	h, ok := f.Glyph('H')
	if !ok {
		t.Fatal("no 'H' glyph")
	}
	if len(h.Strokes) != 3 {
		t.Errorf("'H' has %d strokes, want 3", len(h.Strokes))
	}
	if f.capBase <= f.capTop {
		t.Errorf("cap height is degenerate: top=%v base=%v", f.capTop, f.capBase)
	}

	// 'i' is a dot plus a stem: proof that pen-up markers split strokes.
	i, ok := f.Glyph('i')
	if !ok {
		t.Fatal("no 'i' glyph")
	}
	if len(i.Strokes) < 2 {
		t.Errorf("'i' has %d strokes, want at least 2 (dot and stem)", len(i.Strokes))
	}
}

func TestParseJHFRejectsGarbage(t *testing.T) {
	tests := []struct{ name, data string }{
		{"short line", "abc"},
		{"bad vertex count", "12345 xx MWOMOM"},
		{"truncated vertices", "12345 20 MW"},
		{"no H glyph", "    1  2 MWOM"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseJHF(tc.data); err == nil {
				t.Error("want an error, got nil")
			}
		})
	}
}

func TestScaleTracksCapHeight(t *testing.T) {
	f := Futural()
	const cap = 50.0

	strokes, _ := f.RenderRune('H', geom.Point{X: 0, Y: 0}, cap)
	b, ok := geom.Bounds(strokes)
	if !ok {
		t.Fatal("'H' rendered no points")
	}
	if math.Abs(b.H-cap) > 0.01 {
		t.Errorf("'H' is %.2fpx tall, want the %.0fpx cap height", b.H, cap)
	}
	// Rendering anchors the cap top at the origin's y.
	if math.Abs(b.Y) > 0.01 {
		t.Errorf("'H' top is at y=%.2f, want 0", b.Y)
	}
	// x=0 is the pen position, not the ink: the glyph's left side bearing sits
	// between them, and the ink must stay inside the advance.
	if b.X < 0 {
		t.Errorf("'H' ink starts at x=%.2f, left of the pen position", b.X)
	}
	if adv := f.Advance('H', cap); b.Right() > adv {
		t.Errorf("'H' ink reaches x=%.2f, past its %.2f advance", b.Right(), adv)
	}
}

func TestAdvanceAndMeasure(t *testing.T) {
	f := Futural()
	const cap = 50.0

	if a := f.Advance('W', cap); a <= f.Advance('i', cap) {
		t.Errorf("proportional spacing broken: W advance %.2f <= i advance %.2f", a, f.Advance('i', cap))
	}
	if a := f.Advance(' ', cap); a <= 0 {
		t.Errorf("space advance = %.2f, want positive", a)
	}
	// An uncovered rune still advances, so text does not pile up on itself.
	if a := f.Advance('€', cap); a != missingAdvance*cap {
		t.Errorf("missing-glyph advance = %.2f, want %.2f", a, missingAdvance*cap)
	}

	want := f.Advance('a', cap) + f.Advance('b', cap)
	if got := f.Measure("ab", cap); math.Abs(got-want) > 1e-9 {
		t.Errorf("Measure(\"ab\") = %.4f, want %.4f", got, want)
	}
}

func TestDescentIsPositive(t *testing.T) {
	f := Futural()
	// Descenders are what force line spacing >= 1.8 cap heights (M0).
	if d := f.DescentPx(50); d <= 0 {
		t.Errorf("DescentPx = %.2f, want positive (g/p/y hang below the baseline)", d)
	}
}
