package layout

import (
	"fmt"
	"strings"
	"testing"

	"github.com/momaek/yome/internal/geom"
)

func TestWrapAtWordBoundaries(t *testing.T) {
	f := NewFace(Futural(), nil)
	const cap = 50.0

	text := "the quick brown fox jumps over the lazy dog"
	width := f.Measure("the quick brown", cap) + 1

	lines := Wrap(f, text, cap, width)
	if len(lines) < 2 {
		t.Fatalf("nothing wrapped: %q", lines)
	}
	for i, l := range lines {
		if w := f.Measure(l, cap); w > width {
			t.Errorf("line %d is %.1fpx wide, over the %.1fpx limit: %q", i, w, width, l)
		}
		if strings.HasPrefix(l, " ") || strings.HasSuffix(l, " ") {
			t.Errorf("line %d has edge whitespace: %q", i, l)
		}
	}
	// Wrapping must not lose or invent words.
	if got, want := strings.Join(lines, " "), text; got != want {
		t.Errorf("round trip = %q, want %q", got, want)
	}
}

func TestWrapKeepsHardLineBreaks(t *testing.T) {
	f := NewFace(Futural(), nil)
	lines := Wrap(f, "one\n\ntwo", 50, 10000)
	want := []string{"one", "", "two"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines %q, want %d", len(lines), lines, len(want))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestWrapBreaksAWordTooLongToFit(t *testing.T) {
	f := NewFace(Futural(), nil)
	const cap = 50.0
	width := f.Measure("aaa", cap)

	lines := Wrap(f, "aaaaaaaaaa", cap, width)
	if len(lines) < 2 {
		t.Fatalf("an over-long word was not broken: %q", lines)
	}
	var joined string
	for i, l := range lines {
		if w := f.Measure(l, cap); w > width {
			t.Errorf("piece %d is %.1fpx wide, over the %.1fpx limit: %q", i, w, width, l)
		}
		joined += l
	}
	if joined != "aaaaaaaaaa" {
		t.Errorf("broken word reassembles to %q, want %q", joined, "aaaaaaaaaa")
	}
}

func TestTextOverflowsRatherThanRunningOffThePage(t *testing.T) {
	f := NewFace(Futural(), nil)
	opt := TextOptions{
		CapHeightPx: 50,
		LineSpacing: 1.8,
		// Tall enough for two lines only.
		Area: geom.Rect{X: 0, Y: 0, W: 400, H: f.OvershootPx(50, false) + 50*1.8 + 50 + f.DescentPx(50, false)},
	}
	res := Text(f, "alpha\nbravo\ncharlie\ndelta", opt)

	if len(res.Lines) != 2 {
		t.Errorf("laid out %d lines %q, want 2", len(res.Lines), res.Lines)
	}
	if len(res.Overflow) != 2 {
		t.Errorf("overflow = %q, want 2 lines", res.Overflow)
	}
	if len(res.Lines)+len(res.Overflow) != 4 {
		t.Error("lines went missing between fitted and overflow")
	}

	// Nothing may be drawn below the area: overflow means not injected at all.
	b, ok := geom.Bounds(res.Strokes)
	if !ok {
		t.Fatal("no strokes rendered")
	}
	if b.Bottom() > opt.Area.Bottom() {
		t.Errorf("ink reaches y=%.1f, past the area bottom %.1f", b.Bottom(), opt.Area.Bottom())
	}
}

func TestTextStaysInsideItsArea(t *testing.T) {
	f := NewFace(Futural(), nil)
	opt := TextOptions{
		CapHeightPx: 40,
		LineSpacing: 1.8,
		Area:        geom.Rect{X: 100, Y: 120, W: 1204, H: 1632},
	}
	res := Text(f, strings.Repeat("the quick brown fox jumps over the lazy dog. ", 5), opt)
	if len(res.Strokes) == 0 {
		t.Fatal("no strokes rendered")
	}
	b, _ := geom.Bounds(res.Strokes)
	if b.X < opt.Area.X || b.Right() > opt.Area.Right() {
		t.Errorf("ink spans x %.1f..%.1f, outside the area %.1f..%.1f", b.X, b.Right(), opt.Area.X, opt.Area.Right())
	}
	if b.Y < opt.Area.Y || b.Bottom() > opt.Area.Bottom() {
		t.Errorf("ink spans y %.1f..%.1f, outside the area %.1f..%.1f", b.Y, b.Bottom(), opt.Area.Y, opt.Area.Bottom())
	}
}

func TestLineSpacingIsClampedToTheDescenderFloor(t *testing.T) {
	f := NewFace(Futural(), nil)
	area := geom.Rect{X: 0, Y: 0, W: 1000, H: 1000}

	// A caller asking for 1.0 must still get MinLineSpacing: below that,
	// descenders collide with the next line's caps (M0 finding).
	tight := Text(f, "gp\ngp", TextOptions{CapHeightPx: 50, LineSpacing: 1.0, Area: area})
	floor := Text(f, "gp\ngp", TextOptions{CapHeightPx: 50, LineSpacing: MinLineSpacing, Area: area})

	tb, _ := geom.Bounds(tight.Strokes)
	fb, _ := geom.Bounds(floor.Strokes)
	if tb.H != fb.H {
		t.Errorf("spacing 1.0 produced height %.2f, want the clamped %.2f", tb.H, fb.H)
	}
}

func TestDescendersClearTheNextLine(t *testing.T) {
	f := NewFace(Futural(), nil)
	const cap = 50.0
	// The gap between a descender's lowest point and the next line's cap top.
	gap := cap*MinLineSpacing - (cap + f.DescentPx(cap, false))
	if gap <= 0 {
		t.Errorf("at %.1f spacing, descenders overlap the next line by %.2fpx", MinLineSpacing, -gap)
	}
}

func TestPageBudget(t *testing.T) {
	f := NewFace(Futural(), nil)
	// The M0 estimate: ~550 characters per page of 50px text.
	opt := TextOptions{
		CapHeightPx: 50,
		LineSpacing: 1.8,
		Area:        geom.Rect{X: 100, Y: 120, W: 1204, H: 1632},
	}
	got := PageBudget(f, opt)
	if got < 400 || got > 800 {
		t.Errorf("PageBudget = %d, want roughly the 550 measured in M0", got)
	}

	// Budget must fall as text grows.
	big := opt
	big.CapHeightPx = 100
	if b := PageBudget(f, big); b >= got {
		t.Errorf("budget at 100px cap = %d, not below the %d at 50px", b, got)
	}
}

func TestPageBudgetIsHonestAboutTinyAreas(t *testing.T) {
	f := NewFace(Futural(), nil)
	opt := TextOptions{CapHeightPx: 50, LineSpacing: 1.8, Area: geom.Rect{W: 1000, H: 10}}
	if got := PageBudget(f, opt); got != 0 {
		t.Errorf("PageBudget for an area too short for one line = %d, want 0", got)
	}
}

func TestFitScalesIntoTheAreaPreservingAspect(t *testing.T) {
	// A 2:1 landscape box of strokes into a square area.
	in := []geom.Stroke{{Points: []geom.Point{{X: 0, Y: 0}, {X: 200, Y: 0}, {X: 200, Y: 100}, {X: 0, Y: 100}, {X: 0, Y: 0}}}}
	area := geom.Rect{X: 50, Y: 60, W: 400, H: 400}

	got := Fit(in, FitOptions{Area: area, Scale: 1})
	b, ok := geom.Bounds(got)
	if !ok {
		t.Fatal("Fit dropped everything")
	}
	if b.X != area.X || b.Y != area.Y {
		t.Errorf("Fit anchored at (%.1f,%.1f), want the area's top-left (%.1f,%.1f)", b.X, b.Y, area.X, area.Y)
	}
	if b.W != 400 {
		t.Errorf("width = %.1f, want the area's 400", b.W)
	}
	if b.H != 200 {
		t.Errorf("height = %.1f, want 200 (2:1 aspect preserved)", b.H)
	}
}

func TestFitScaleFraction(t *testing.T) {
	in := []geom.Stroke{{Points: []geom.Point{{X: 0, Y: 0}, {X: 100, Y: 100}}}}
	area := geom.Rect{X: 0, Y: 0, W: 400, H: 400}
	got := Fit(in, FitOptions{Area: area, Scale: 0.5})
	b, _ := geom.Bounds(got)
	if b.W != 200 {
		t.Errorf("at scale 0.5 width = %.1f, want 200", b.W)
	}
}

func TestFitHandlesDegenerateInput(t *testing.T) {
	area := geom.Rect{X: 0, Y: 0, W: 400, H: 400}
	if got := Fit(nil, FitOptions{Area: area}); got != nil {
		t.Errorf("Fit(nil) = %+v, want nil", got)
	}
	// A perfectly horizontal line has zero height; scaling must not divide by it.
	flat := []geom.Stroke{{Points: []geom.Point{{X: 0, Y: 5}, {X: 100, Y: 5}}}}
	got := Fit(flat, FitOptions{Area: area, Scale: 1})
	b, ok := geom.Bounds(got)
	if !ok {
		t.Fatal("Fit dropped a horizontal line")
	}
	if b.W != 400 {
		t.Errorf("horizontal line scaled to %.1f wide, want 400", b.W)
	}
}

func TestFitClipsToTheArea(t *testing.T) {
	area := geom.Rect{X: 0, Y: 0, W: 100, H: 100}
	in := []geom.Stroke{{Points: []geom.Point{{X: 0, Y: 0}, {X: 10, Y: 10}}}}
	for _, s := range Fit(in, FitOptions{Area: area, Scale: 1}) {
		for _, p := range s.Points {
			if !area.Contains(p) {
				t.Errorf("point %+v escaped the area", p)
			}
		}
	}
}

func ExampleWrap() {
	f := NewFace(Futural(), nil)
	for _, l := range Wrap(f, "the quick brown fox jumps over the lazy dog", 50, 500) {
		fmt.Printf("%q\n", l)
	}
	// Output:
	// "the quick"
	// "brown fox"
	// "jumps over"
	// "the lazy dog"
}
