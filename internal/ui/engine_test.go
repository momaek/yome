package ui

import (
	"errors"
	"image"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/momaek/yome/internal/geom"
)

// fakeScreen serves pixels, ink counts, and region ratios from a scripted
// state.
type fakeScreen struct {
	dark   map[[2]int]bool             // sample points that read as dark
	ratios map[image.Rectangle]float64 // scripted region ink ratios
	ink    int
	err    error
	// onRead flips state as taps take effect, letting a test model the UI
	// responding a moment after a press.
	reads int
}

func (f *fakeScreen) Pixel(x, y int) (uint8, error) {
	f.reads++
	if f.err != nil {
		return 0, f.err
	}
	if f.dark[[2]int{x, y}] {
		return 0, nil
	}
	return 255, nil
}

func (f *fakeScreen) InkCount() (int, error) {
	f.reads++
	return f.ink, f.err
}

func (f *fakeScreen) InkRatioRect(r image.Rectangle) (float64, error) {
	f.reads++
	if f.err != nil {
		return 0, f.err
	}
	return f.ratios[r], nil
}

// fakeTapper records taps and can drive screen state changes.
type fakeTapper struct {
	taps  []geom.Point
	holds []time.Duration
	err   error
	after func(p geom.Point) // called once a tap lands, to update the screen
}

func (f *fakeTapper) Tap(p geom.Point, hold time.Duration) error {
	if f.err != nil {
		return f.err
	}
	f.taps = append(f.taps, p)
	f.holds = append(f.holds, hold)
	if f.after != nil {
		f.after(p)
	}
	return nil
}

func testVersion() *Version {
	return &Version{
		Meta: Meta{TapMs: 80, TapLongMs: 200, TapGapMinMs: 0, ProbePollMs: 1, ProbeTimeoutMs: 20},
		Probes: map[string]Probe{
			"toolbar_open": {At: []int{55, 155}, Expect: "dark"},
			"panel_open":   {At: []int{345, 130}, Expect: "dark"},
			"page_light":   {At: []int{10, 10}, Expect: "light"},
			"blank_page":   {BlackMax: ptr(4000)},
			"malformed":    {Expect: "dark"},
			"bad_expect":   {At: []int{1, 1}, Expect: "purple"},
		},
		Controls: map[string]Control{
			"toolbar_toggle": {Tap: []int{55, 52}, SkipIf: "toolbar_open"},
			"tool_pen":       {Tap: []int{55, 155}, Requires: "toolbar_open"},
			"tool_eraser":    {Tap: []int{55, 365}, Requires: "toolbar_open"},
			"pen_panel":      {Tap: []int{55, 155}, Hold: "long", Requires: "toolbar_open", Verify: "panel_open"},
			"pen_marker":     {Tap: []int{189, 341}},
			"no_point":       {},
			"no_skip_state":  {Tap: []int{1, 1}},
		},
		Features: map[string][]string{
			"select_pen":      {"toolbar_toggle?", "tool_pen", "toolbar_toggle"},
			"select_pen_type": {"toolbar_toggle?", "tool_pen", "pen_panel", "<pen_type>", "toolbar_toggle"},
			"bad_conditional": {"no_skip_state?"},
			"unknown_control": {"nope"},
		},
	}
}

func ptr[T any](v T) *T { return &v }

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestEngine(scr *fakeScreen, tap *fakeTapper) *Engine {
	return New(testVersion(), tap, scr, quietLogger())
}

func TestProbePixel(t *testing.T) {
	scr := &fakeScreen{dark: map[[2]int]bool{{55, 155}: true}}
	e := newTestEngine(scr, &fakeTapper{})

	if ok, err := e.Probe("toolbar_open"); err != nil || !ok {
		t.Errorf("toolbar_open = %v, %v; want true", ok, err)
	}
	// A light pixel where dark is expected means the state does not hold.
	if ok, err := e.Probe("panel_open"); err != nil || ok {
		t.Errorf("panel_open = %v, %v; want false", ok, err)
	}
	// Probes expecting light invert the test.
	if ok, err := e.Probe("page_light"); err != nil || !ok {
		t.Errorf("page_light = %v, %v; want true", ok, err)
	}
}

func TestProbeInkCount(t *testing.T) {
	e := newTestEngine(&fakeScreen{ink: 100}, &fakeTapper{})
	if ok, _ := e.Probe("blank_page"); !ok {
		t.Error("100 ink pixels should read as a blank page")
	}

	e = newTestEngine(&fakeScreen{ink: 50000}, &fakeTapper{})
	if ok, _ := e.Probe("blank_page"); ok {
		t.Error("50000 ink pixels should not read as a blank page")
	}
}

func TestProbeErrors(t *testing.T) {
	tests := []struct{ name, probe, want string }{
		{"unknown probe", "nope", "unknown probe"},
		{"no sample point", "malformed", "black_max"},
		{"bad expect value", "bad_expect", "expect"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(&fakeScreen{}, &fakeTapper{})
			_, err := e.Probe(tc.probe)
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestProbePropagatesScreenErrors(t *testing.T) {
	boom := errors.New("framebuffer went away")
	e := newTestEngine(&fakeScreen{err: boom}, &fakeTapper{})
	if _, err := e.Probe("toolbar_open"); !errors.Is(err, boom) {
		t.Errorf("error %v does not wrap the screen's error", err)
	}
}

// The whole point of `requires`: if the UI is not in the expected state, the
// tap must not happen. Blind-tapping an unknown screen can hit anything.
func TestTapRefusesWhenItsPreconditionFails(t *testing.T) {
	tap := &fakeTapper{}
	e := newTestEngine(&fakeScreen{}, tap) // toolbar closed

	err := e.Tap("tool_pen")
	if err == nil {
		t.Fatal("tapped a control whose precondition does not hold")
	}
	if !strings.Contains(err.Error(), "toolbar_open") {
		t.Errorf("error %q does not name the missing state", err)
	}
	if len(tap.taps) != 0 {
		t.Errorf("emitted %d taps despite the failed precondition", len(tap.taps))
	}
}

func TestTapUsesTheRightHold(t *testing.T) {
	scr := &fakeScreen{dark: map[[2]int]bool{{55, 155}: true, {345, 130}: true}}
	tap := &fakeTapper{}
	e := newTestEngine(scr, tap)

	if err := e.Tap("tool_pen"); err != nil {
		t.Fatal(err)
	}
	if tap.holds[0] != 80*time.Millisecond {
		t.Errorf("ordinary tap held %v, want 80ms", tap.holds[0])
	}

	// Opening an options panel needs the long hold: 60ms does nothing (M0).
	if err := e.Tap("pen_panel"); err != nil {
		t.Fatal(err)
	}
	if tap.holds[1] != 200*time.Millisecond {
		t.Errorf("panel tap held %v, want the 200ms long press", tap.holds[1])
	}
}

func TestTapVerifiesTheTransition(t *testing.T) {
	scr := &fakeScreen{dark: map[[2]int]bool{{55, 155}: true}}
	tap := &fakeTapper{}
	// The panel opens only once the tap lands, as on a real device.
	tap.after = func(p geom.Point) {
		if p.X == 55 && p.Y == 155 {
			scr.dark[[2]int{345, 130}] = true
		}
	}
	e := newTestEngine(scr, tap)

	if err := e.Tap("pen_panel"); err != nil {
		t.Fatalf("pen_panel: %v", err)
	}
}

func TestTapFailsWhenTheTransitionNeverHappens(t *testing.T) {
	scr := &fakeScreen{dark: map[[2]int]bool{{55, 155}: true}} // panel never opens
	e := newTestEngine(scr, &fakeTapper{})

	err := e.Tap("pen_panel")
	if err == nil {
		t.Fatal("a tap that had no effect reported success")
	}
	if !strings.Contains(err.Error(), "panel_open") {
		t.Errorf("error %q does not name the state that never arrived", err)
	}
}

func TestTapPropagatesInjectionErrors(t *testing.T) {
	boom := errors.New("touch device is gone")
	e := newTestEngine(&fakeScreen{}, &fakeTapper{err: boom})
	if err := e.Tap("toolbar_toggle"); !errors.Is(err, boom) {
		t.Errorf("error %v does not wrap the tapper's error", err)
	}
}

func TestTapErrors(t *testing.T) {
	e := newTestEngine(&fakeScreen{}, &fakeTapper{})
	if err := e.Tap("nope"); err == nil {
		t.Error("tapping an unknown control reported success")
	}
	if err := e.Tap("no_point"); err == nil {
		t.Error("tapping a control with no coordinates reported success")
	}
}

func TestRunSkipsAConditionalStepWhenTheStateHolds(t *testing.T) {
	// Toolbar already open: select_pen must not toggle it open again.
	scr := &fakeScreen{dark: map[[2]int]bool{{55, 155}: true}}
	tap := &fakeTapper{}
	e := newTestEngine(scr, tap)

	if err := e.Run("select_pen", nil); err != nil {
		t.Fatal(err)
	}
	want := []geom.Point{{X: 55, Y: 155}, {X: 55, Y: 52}} // tool_pen, then close
	assertTaps(t, tap.taps, want)
}

func TestRunTakesAConditionalStepWhenTheStateDoesNot(t *testing.T) {
	scr := &fakeScreen{} // toolbar closed
	tap := &fakeTapper{}
	// Opening the toolbar reveals the pen icon.
	tap.after = func(p geom.Point) {
		if p.X == 55 && p.Y == 52 {
			scr.dark[[2]int{55, 155}] = true
		}
	}
	scr.dark = map[[2]int]bool{}
	e := newTestEngine(scr, tap)

	if err := e.Run("select_pen", nil); err != nil {
		t.Fatal(err)
	}
	want := []geom.Point{{X: 55, Y: 52}, {X: 55, Y: 155}, {X: 55, Y: 52}}
	assertTaps(t, tap.taps, want)
}

func TestRunSubstitutesParameters(t *testing.T) {
	scr := &fakeScreen{dark: map[[2]int]bool{{55, 155}: true, {345, 130}: true}}
	tap := &fakeTapper{}
	e := newTestEngine(scr, tap)

	if err := e.Run("select_pen_type", map[string]string{"pen_type": "pen_marker"}); err != nil {
		t.Fatal(err)
	}
	want := []geom.Point{
		{X: 55, Y: 155},  // tool_pen
		{X: 55, Y: 155},  // pen_panel (long hold)
		{X: 189, Y: 341}, // pen_marker, the substituted control
		{X: 55, Y: 52},   // close the toolbar
	}
	assertTaps(t, tap.taps, want)
}

func TestRunErrors(t *testing.T) {
	tests := []struct {
		name    string
		feature string
		params  map[string]string
		want    string
	}{
		{"unknown feature", "nope", nil, "unknown feature"},
		{"unknown control in a step", "unknown_control", nil, "unknown control"},
		{"missing parameter", "select_pen_type", nil, "parameter"},
		{"conditional without skip_if", "bad_conditional", nil, "skip_if"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scr := &fakeScreen{dark: map[[2]int]bool{{55, 155}: true, {345, 130}: true}}
			e := newTestEngine(scr, &fakeTapper{})
			err := e.Run(tc.feature, tc.params)
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestRunStopsAtTheFirstFailedStep(t *testing.T) {
	scr := &fakeScreen{} // toolbar closed and it never opens
	tap := &fakeTapper{}
	e := newTestEngine(scr, tap)

	if err := e.Run("select_pen", nil); err == nil {
		t.Fatal("want an error when the toolbar never opens")
	}
	// It taps the toggle, then finds tool_pen's precondition unmet and stops.
	if len(tap.taps) != 1 {
		t.Errorf("emitted %d taps, want 1 before aborting: %v", len(tap.taps), tap.taps)
	}
}

func TestWaitProbeTimesOut(t *testing.T) {
	e := newTestEngine(&fakeScreen{}, &fakeTapper{})
	start := time.Now()
	err := e.WaitProbe("toolbar_open")
	if err == nil {
		t.Fatal("waiting for a state that never arrives reported success")
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Errorf("gave up after %v, before the 20ms timeout", elapsed)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error %q does not read as a timeout", err)
	}
}

func TestWaitProbeReturnsAsSoonAsTheStateArrives(t *testing.T) {
	scr := &fakeScreen{dark: map[[2]int]bool{{55, 155}: true}}
	e := newTestEngine(scr, &fakeTapper{})
	if err := e.WaitProbe("toolbar_open"); err != nil {
		t.Fatal(err)
	}
	// Polling, not sleeping: one read is enough when the state already holds.
	if scr.reads != 1 {
		t.Errorf("polled %d times for an already-true state, want 1", scr.reads)
	}
}

func TestListings(t *testing.T) {
	e := newTestEngine(&fakeScreen{}, &fakeTapper{})
	if got := e.Features(); len(got) == 0 || got[0] != "bad_conditional" {
		t.Errorf("Features() = %v, want a sorted list", got)
	}
	if got := e.Probes(); len(got) == 0 || got[0] != "bad_expect" {
		t.Errorf("Probes() = %v, want a sorted list", got)
	}
	if got := e.Controls(); len(got) == 0 || got[0] != "no_point" {
		t.Errorf("Controls() = %v, want a sorted list", got)
	}
}

func assertTaps(t *testing.T, got, want []geom.Point) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d taps %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("tap %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// --- firmware 3.27 additions: region probes, orientation, landscape ---

// blockVersion is a map slice with 3.27-style region probes and landscape
// overrides.
func blockVersion() *Version {
	return &Version{
		Meta: Meta{TapMs: 80, TapLongMs: 600, TapGapMinMs: 0, ProbePollMs: 1, ProbeTimeoutMs: 20},
		Probes: map[string]Probe{
			"pen_selected":          {Region: []int{0, 112, 110, 222}, Expect: "dark_block"},
			"region_bad_expect":     {Region: []int{0, 0, 10, 10}, Expect: "dark"},
			"orientation_portrait":  {Region: []int{0, 112, 110, 950}, Expect: "dark_block", Ratio: ptr(0.05)},
			"orientation_landscape": {Region: []int{100, 1750, 1000, 1872}, Expect: "dark_block", Ratio: ptr(0.05)},
		},
		Controls: map[string]Control{
			"tool_pen":  {Tap: []int{55, 167}},
			"menu_more": {Tap: []int{55, 1816}},
		},
		LandscapeOverrides: map[string]Control{
			"menu_more": {Tap: []int{1348, 1815}}, // physical, re-measured
		},
		Features: map[string][]string{"select_pen": {"tool_pen"}},
	}
}

func TestProbeDarkBlock(t *testing.T) {
	scr := &fakeScreen{ratios: map[image.Rectangle]float64{
		image.Rect(0, 112, 110, 222): 0.9, // selected tool: inverted icon block
	}}
	e := New(blockVersion(), &fakeTapper{}, scr, quietLogger())

	if ok, err := e.Probe("pen_selected"); err != nil || !ok {
		t.Errorf("pen_selected over a 0.9-dark block = %v, %v; want true", ok, err)
	}

	scr.ratios[image.Rect(0, 112, 110, 222)] = 0.1 // deselected: mostly paper
	if ok, err := e.Probe("pen_selected"); err != nil || ok {
		t.Errorf("pen_selected over a 0.1-dark block = %v, %v; want false", ok, err)
	}
}

func TestProbeRegionRejectsWrongExpect(t *testing.T) {
	e := New(blockVersion(), &fakeTapper{}, &fakeScreen{}, quietLogger())
	if _, err := e.Probe("region_bad_expect"); err == nil || !strings.Contains(err.Error(), "dark_block") {
		t.Errorf("a region probe with expect=dark should demand dark_block, got %v", err)
	}
}

func TestDetectOrientation(t *testing.T) {
	portraitBar := image.Rect(0, 112, 110, 950)
	landscapeBar := image.Rect(100, 1750, 1000, 1872)

	tests := []struct {
		name    string
		ratios  map[image.Rectangle]float64
		want    Orientation
		wantErr bool
	}{
		// Measured 2026-07-18 on 3.27.3.0.
		{"portrait", map[image.Rectangle]float64{portraitBar: 0.94, landscapeBar: 0.0}, Portrait, false},
		{"landscape", map[image.Rectangle]float64{portraitBar: 0.0, landscapeBar: 0.138}, Landscape, false},
		{"no toolbar anywhere", map[image.Rectangle]float64{portraitBar: 0.0, landscapeBar: 0.0}, Portrait, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := New(blockVersion(), &fakeTapper{}, &fakeScreen{ratios: tc.ratios}, quietLogger())
			got, err := e.DetectOrientation()
			if tc.wantErr {
				if err == nil {
					t.Fatal("want an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("DetectOrientation = %v, want %v", got, tc.want)
			}
			if e.Orientation() != tc.want {
				t.Errorf("engine did not adopt the detected orientation")
			}
		})
	}
}

func TestDetectOrientationWithoutProbesAssumesPortrait(t *testing.T) {
	// 3.11 has no landscape calibration; its map must keep working.
	e := newTestEngine(&fakeScreen{}, &fakeTapper{})
	got, err := e.DetectOrientation()
	if err != nil || got != Portrait {
		t.Errorf("DetectOrientation on a probe-less map = %v, %v; want portrait, nil", got, err)
	}
}

func TestLandscapeTapRotatesCoordinates(t *testing.T) {
	tap := &fakeTapper{}
	e := New(blockVersion(), tap, &fakeScreen{}, quietLogger())
	e.SetOrientation(Landscape)

	// Main-column control: pure view rotation, phys = (view_y, 1871-view_x).
	if err := e.Tap("tool_pen"); err != nil {
		t.Fatal(err)
	}
	if want := (geom.Point{X: 167, Y: 1816}); tap.taps[0] != want {
		t.Errorf("landscape tool_pen tapped %v, want the rotated %v", tap.taps[0], want)
	}

	// Overridden control: xochitl re-arranges it, so the measured physical
	// point wins over the formula (which would land off the toolbar).
	if err := e.Tap("menu_more"); err != nil {
		t.Fatal(err)
	}
	if want := (geom.Point{X: 1348, Y: 1815}); tap.taps[1] != want {
		t.Errorf("landscape menu_more tapped %v, want the override %v", tap.taps[1], want)
	}
}

func TestLandscapeProbeRotatesRegions(t *testing.T) {
	// pen_selected region [0,112,110,222] rotates to phys [112,1762,222,1872].
	scr := &fakeScreen{ratios: map[image.Rectangle]float64{
		image.Rect(112, 1872-110, 222, 1872): 0.9,
	}}
	e := New(blockVersion(), &fakeTapper{}, scr, quietLogger())
	e.SetOrientation(Landscape)

	if ok, err := e.Probe("pen_selected"); err != nil || !ok {
		t.Errorf("landscape pen_selected = %v, %v; want true via the rotated region", ok, err)
	}
}

func TestPortraitTapIsUntransformed(t *testing.T) {
	tap := &fakeTapper{}
	e := New(blockVersion(), tap, &fakeScreen{}, quietLogger())
	if err := e.Tap("menu_more"); err != nil {
		t.Fatal(err)
	}
	if want := (geom.Point{X: 55, Y: 1816}); tap.taps[0] != want {
		t.Errorf("portrait menu_more tapped %v, want the calibrated %v", tap.taps[0], want)
	}
}
