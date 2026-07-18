package layout

import (
	"strings"
	"testing"

	"github.com/momaek/yome/internal/geom"
)

func testFace(t *testing.T) *Face {
	t.Helper()
	han, err := Han()
	if err != nil {
		t.Fatalf("embedded CJK data failed to parse: %v", err)
	}
	return NewFace(Futural(), han)
}

func TestCJKTableCoverage(t *testing.T) {
	han, err := Han()
	if err != nil {
		t.Fatal(err)
	}
	if han.Len() < 9000 {
		t.Errorf("table has %d characters, want the full ~9500 simplified set", han.Len())
	}
	// The characters the project will write constantly must exist.
	for _, r := range "的一是不了人我在有他这中文写回设备笔记" {
		if !han.Has(r) {
			t.Errorf("table is missing %q", r)
		}
	}
}

func TestCJKRenderGeometry(t *testing.T) {
	han, err := Han()
	if err != nil {
		t.Fatal(err)
	}
	const em = 100.0
	origin := geom.Point{X: 200, Y: 300}
	strokes, ok := han.Render('中', origin, em)
	if !ok {
		t.Fatal("中 did not render")
	}
	if len(strokes) != 4 {
		t.Errorf("中 rendered %d strokes, want its 4", len(strokes))
	}
	b, _ := geom.Bounds(strokes)
	if b.X < origin.X || b.Y < origin.Y || b.Right() > origin.X+em || b.Bottom() > origin.Y+em {
		t.Errorf("中 bounds %+v leak outside the em box at %+v size %v", b, origin, em)
	}
	// The spline pass must keep strokes dense enough to look smooth: the raw
	// medians carry only a handful of points.
	for i, s := range strokes {
		if len(s.Points) < 5 {
			t.Errorf("stroke %d has only %d points; smoothing did not run", i, len(s.Points))
		}
	}
}

func TestSmoothPassesThroughEndpoints(t *testing.T) {
	pts := []geom.Point{{X: 0, Y: 0}, {X: 50, Y: 10}, {X: 100, Y: 0}}
	sm := smooth(pts)
	if sm[0] != pts[0] || sm[len(sm)-1] != pts[len(pts)-1] {
		t.Errorf("smoothing moved the endpoints: %v .. %v", sm[0], sm[len(sm)-1])
	}
	// It must also pass through the interior control point.
	found := false
	for _, p := range sm {
		if p == pts[1] {
			found = true
		}
	}
	if !found {
		t.Error("smoothing does not pass through the middle point")
	}
}

func TestFaceMixedMetrics(t *testing.T) {
	f := testFace(t)
	const cap = 50.0

	if adv := f.Advance('中', cap); adv != DefaultHanScale*cap {
		t.Errorf("han advance = %v, want %v", adv, DefaultHanScale*cap)
	}
	if adv := f.Advance('A', cap); adv <= 0 || adv == DefaultHanScale*cap {
		t.Errorf("latin advance = %v looks wrong", adv)
	}
	// Han glyphs rise above the cap line and sink below the baseline, so
	// mixed-text clearance must grow when han participates.
	if f.OvershootPx(cap, true) <= f.Latin.OvershootPx(cap) {
		t.Error("han overshoot did not raise the clearance")
	}
	if f.DescentPx(cap, true) <= 0 {
		t.Error("han descent missing")
	}
	// ...and pure-Latin metrics must be untouched by the han table's presence.
	if f.OvershootPx(cap, false) != f.Latin.OvershootPx(cap) {
		t.Error("latin-only overshoot changed")
	}
}

func TestFullwidthPunctuationFallsBack(t *testing.T) {
	f := testFace(t)
	strokes, adv := f.RenderRune('，', geom.Point{}, 50)
	if len(strokes) == 0 || adv <= 0 {
		t.Error("， rendered nothing; should degrade to the latin comma")
	}
	if adv == DefaultHanScale*50 {
		t.Error("， took a full han em; should use the latin comma's advance")
	}
}

func TestWrapChineseBreaksAnywhere(t *testing.T) {
	f := testFace(t)
	const cap = 50.0
	text := "今天天气很好我们去公园散步"
	// Width for exactly four han characters.
	width := 4*DefaultHanScale*cap + 1

	lines := Wrap(f, text, cap, width)
	if len(lines) != 4 { // 13 chars / 4 per line
		t.Fatalf("wrapped into %d lines %q, want 4", len(lines), lines)
	}
	for i, l := range lines {
		if w := f.Measure(l, cap); w > width {
			t.Errorf("line %d is %.1fpx, over %.1fpx: %q", i, w, width, l)
		}
	}
	if got := strings.Join(lines, ""); got != text {
		t.Errorf("round trip = %q, want %q", got, text)
	}
}

func TestWrapMixedTextKeepsLatinWordsWhole(t *testing.T) {
	f := testFace(t)
	const cap = 50.0
	text := "用 reMarkable 写字很好"

	// Wide enough for everything: one line, with the source spacing kept.
	lines := Wrap(f, text, cap, 10000)
	if len(lines) != 1 || lines[0] != text {
		t.Fatalf("wide wrap = %q, want the text unchanged", lines)
	}

	// Too narrow for the latin word + trailing han run: the word must move to
	// its own line rather than split.
	width := f.Measure("用 reMarkable", cap) + 1
	lines = Wrap(f, text, cap, width)
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "reMarkable") {
		t.Errorf("the latin word was split: %q", lines)
	}
}

func TestTextRendersChinese(t *testing.T) {
	f := testFace(t)
	res := Text(f, "中文写回", TextOptions{
		CapHeightPx: 50,
		LineSpacing: 1.8,
		Area:        geom.Rect{X: 100, Y: 100, W: 1000, H: 500},
	})
	if len(res.Overflow) != 0 {
		t.Fatalf("four characters overflowed a huge area: %v", res.Overflow)
	}
	if len(res.Strokes) != 19 { // 中4 + 文4 + 写5 + 回6
		t.Errorf("四字 produced %d strokes, want 19", len(res.Strokes))
	}
	b, _ := geom.Bounds(res.Strokes)
	area := geom.Rect{X: 100, Y: 100, W: 1000, H: 500}
	if b.X < area.X || b.Y < area.Y || b.Bottom() > area.Bottom() {
		t.Errorf("strokes leak outside the area: %+v", b)
	}
}

func TestHanPageBudget(t *testing.T) {
	f := testFace(t)
	opt := TextOptions{CapHeightPx: 71, LineSpacing: 1.8, Area: geom.Rect{X: 150, Y: 120, W: 1872 - 250, H: 1404 - 240}}
	got := HanPageBudget(f, opt)
	// The plan's rule of thumb: ~200 chars/page at 100px ems. 71px cap gives
	// ~100px ems on the landscape page; sanity-band the estimate.
	if got < 100 || got > 400 {
		t.Errorf("han page budget = %d, outside the plausible band", got)
	}
	if lat := PageBudget(f, opt); lat <= got {
		t.Errorf("latin budget %d should exceed han budget %d", lat, got)
	}
}
