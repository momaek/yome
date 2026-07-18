package ui

import (
	"fmt"
	"image"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/momaek/yome/internal/capture"
	"github.com/momaek/yome/internal/geom"
)

// Tapper is the subset of inject.Injector the engine needs.
type Tapper interface {
	Tap(p geom.Point, hold time.Duration) error
}

// Screen is the subset of capture.Framebuffer the engine needs.
type Screen interface {
	Pixel(x, y int) (uint8, error)
	InkCount() (int, error)
	InkRatioRect(r image.Rectangle) (float64, error)
}

// Orientation is the notebook's current view rotation. It is a property of
// each notebook, so it must be re-detected at session start and after every
// page or document change — never cached across them (3.27 calibration note).
type Orientation int

const (
	Portrait Orientation = iota
	Landscape
)

func (o Orientation) String() string {
	if o == Landscape {
		return "landscape"
	}
	return "portrait"
}

// Engine executes UI features: probe the precondition, tap, verify the
// transition.
type Engine struct {
	ver    *Version
	tap    Tapper
	scr    Screen
	log    *slog.Logger
	orient Orientation
}

// New builds an engine for one firmware version's calibration.
func New(ver *Version, tap Tapper, scr Screen, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{ver: ver, tap: tap, scr: scr, log: log}
}

// SetOrientation tells the engine how the current notebook is rotated. In
// landscape, calibrated coordinates are treated as view coordinates and
// rotated onto the physical frame, with landscape_overrides taking precedence
// for the controls xochitl re-arranges.
func (e *Engine) SetOrientation(o Orientation) { e.orient = o }

// Orientation returns the engine's current setting (not a fresh detection).
func (e *Engine) Orientation() Orientation { return e.orient }

// point maps a calibrated view-space point to physical coordinates.
func (e *Engine) point(p geom.Point) geom.Point {
	if e.orient == Landscape {
		return geom.LandscapeToPortrait(p)
	}
	return p
}

// rect maps a calibrated view-space rectangle to physical coordinates.
func (e *Engine) rect(r image.Rectangle) image.Rectangle {
	if e.orient == Landscape {
		return image.Rect(r.Min.Y, geom.ScreenH-r.Max.X, r.Max.Y, geom.ScreenH-r.Min.X)
	}
	return r
}

// Probe evaluates a named screen state once, in the engine's current
// orientation.
func (e *Engine) Probe(name string) (bool, error) {
	p, ok := e.ver.Probes[name]
	if !ok {
		return false, fmt.Errorf("unknown probe %q", name)
	}

	if p.BlackMax != nil {
		n, err := e.scr.InkCount()
		if err != nil {
			return false, fmt.Errorf("probe %q: %w", name, err)
		}
		return n <= *p.BlackMax, nil
	}

	if r, ok := p.Rect(); ok {
		if p.Expect != "dark_block" {
			return false, fmt.Errorf("probe %q: a region probe's expect must be \"dark_block\", got %q", name, p.Expect)
		}
		ratio, err := e.scr.InkRatioRect(e.rect(r))
		if err != nil {
			return false, fmt.Errorf("probe %q: %w", name, err)
		}
		return ratio > p.BlockRatio(), nil
	}

	pt, ok := p.Point()
	if !ok {
		return false, fmt.Errorf("probe %q has no `at` point, `region`, or `black_max`", name)
	}
	pt = e.point(pt)
	g, err := e.scr.Pixel(int(pt.X), int(pt.Y))
	if err != nil {
		return false, fmt.Errorf("probe %q: %w", name, err)
	}
	dark := g < capture.BlackThreshold
	switch p.Expect {
	case "dark":
		return dark, nil
	case "light":
		return !dark, nil
	default:
		return false, fmt.Errorf("probe %q: expect must be \"dark\" or \"light\", got %q", name, p.Expect)
	}
}

// DetectOrientation reads the toolbar's physical position to tell how the
// current notebook is rotated, and points the engine at the result.
//
// It compares the ink ratio of two *physical* regions (the portrait toolbar
// column on the left edge vs the landscape toolbar row along the bottom),
// named by the orientation_portrait / orientation_landscape probes. These
// regions are absolute — they are read directly, never view-rotated.
// Calibrated 3.27.3.0: portrait 0.94 vs 0.000, landscape 0.138 vs 0.000.
//
// Maps without these probes (3.11 has no landscape support) report Portrait.
func (e *Engine) DetectOrientation() (Orientation, error) {
	pp, okP := e.ver.Probes["orientation_portrait"]
	pl, okL := e.ver.Probes["orientation_landscape"]
	if !okP || !okL {
		e.log.Debug("map has no orientation probes; assuming portrait")
		e.orient = Portrait
		return Portrait, nil
	}
	rp, ok := pp.Rect()
	if !ok {
		return Portrait, fmt.Errorf("probe orientation_portrait has no region")
	}
	rl, ok := pl.Rect()
	if !ok {
		return Portrait, fmt.Errorf("probe orientation_landscape has no region")
	}
	ratioP, err := e.scr.InkRatioRect(rp)
	if err != nil {
		return Portrait, fmt.Errorf("orientation: %w", err)
	}
	ratioL, err := e.scr.InkRatioRect(rl)
	if err != nil {
		return Portrait, fmt.Errorf("orientation: %w", err)
	}
	e.log.Debug("orientation probe", "portrait_ratio", ratioP, "landscape_ratio", ratioL)

	minRatio := pp.BlockRatio() // both probes carry the same floor
	switch {
	case ratioP >= minRatio && ratioP > 2*ratioL:
		e.orient = Portrait
		return Portrait, nil
	case ratioL >= minRatio && ratioL > 2*ratioP:
		e.orient = Landscape
		return Landscape, nil
	default:
		return Portrait, fmt.Errorf(
			"toolbar not found in either orientation (portrait ratio %.3f, landscape %.3f): not in a notebook view, or the UI has changed — recapture before acting",
			ratioP, ratioL)
	}
}

// WaitProbe polls a probe until it holds or the timeout expires.
//
// Polling rather than sleeping a fixed interval is deliberate: the frame in
// memory settles well before the e-ink panel finishes redrawing, so this
// usually returns in 200-400ms instead of waiting out a worst case.
func (e *Engine) WaitProbe(name string) error {
	deadline := time.Now().Add(e.ver.Meta.ProbeTimeout())
	for {
		ok, err := e.Probe(name)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for state %q; the UI is not where the map says it is — recapture the screen before acting",
				e.ver.Meta.ProbeTimeout(), name)
		}
		time.Sleep(e.ver.Meta.ProbePoll())
	}
}

// controlPoint resolves a control's physical tap point in the current
// orientation: a landscape override wins (already physical, measured);
// everything else is the pure view rotation (calibrated ≤1px on 3.27).
func (e *Engine) controlPoint(name string, c Control) (geom.Point, bool) {
	if e.orient == Landscape {
		if o, ok := e.ver.LandscapeOverrides[name]; ok {
			return o.Point()
		}
	}
	pt, ok := c.Point()
	if !ok {
		return geom.Point{}, false
	}
	return e.point(pt), true
}

// Tap presses a named control, checking its precondition first and its
// postcondition after.
func (e *Engine) Tap(name string) error {
	c, ok := e.ver.Controls[name]
	if !ok {
		return fmt.Errorf("unknown control %q", name)
	}
	pt, ok := e.controlPoint(name, c)
	if !ok {
		return fmt.Errorf("control %q has no `tap` point", name)
	}

	if c.Requires != "" {
		ok, err := e.Probe(c.Requires)
		if err != nil {
			return fmt.Errorf("control %q precondition: %w", name, err)
		}
		if !ok {
			return fmt.Errorf("control %q needs state %q, which does not hold — refusing to tap blind", name, c.Requires)
		}
	}

	hold := e.ver.Meta.Tap()
	if c.Hold == "long" {
		hold = e.ver.Meta.TapLong()
	}
	e.log.Debug("ui tap", "control", name, "at", []float64{pt.X, pt.Y}, "hold", hold)
	if err := e.tap.Tap(pt, hold); err != nil {
		return fmt.Errorf("control %q: %w", name, err)
	}

	// Always leave a gap before the next tap: back-to-back presses race
	// xochitl's own state machine and can register as a double tap.
	time.Sleep(e.ver.Meta.TapGapMin())

	if c.Verify != "" {
		if err := e.WaitProbe(c.Verify); err != nil {
			return fmt.Errorf("control %q did not take effect: %w", name, err)
		}
	}
	return nil
}

// Run executes a named feature: a sequence of control steps.
//
// Step syntax:
//
//	"control"    tap it
//	"control?"   tap it unless its skip_if state already holds
//	"<param>"    tap the control named by params["param"]
func (e *Engine) Run(feature string, params map[string]string) error {
	_, err := e.run(feature, params)
	return err
}

// run is Run, also reporting how many taps it actually performed. Callers use
// the count to decide whether xochitl needs settling time: after a toolbar
// tap, injected pen input is discarded for ~3s (measured 2026-07-18 on
// 3.27.3.0), so a zero-tap run means writing can start immediately.
func (e *Engine) run(feature string, params map[string]string) (taps int, err error) {
	steps, ok := e.ver.Features[feature]
	if !ok {
		return 0, fmt.Errorf("unknown feature %q", feature)
	}
	e.log.Debug("ui run", "feature", feature, "steps", len(steps))

	for i, step := range steps {
		name, conditional := strings.CutSuffix(step, "?")

		if p, ok := strings.CutPrefix(name, "<"); ok {
			key := strings.TrimSuffix(p, ">")
			v, ok := params[key]
			if !ok {
				return taps, fmt.Errorf("feature %q step %d needs parameter %q", feature, i+1, key)
			}
			name = v
		}

		if conditional {
			c, ok := e.ver.Controls[name]
			if !ok {
				return taps, fmt.Errorf("feature %q step %d: unknown control %q", feature, i+1, name)
			}
			if c.SkipIf == "" {
				return taps, fmt.Errorf("feature %q step %d: %q is conditional but the map gives it no skip_if state", feature, i+1, name)
			}
			skip, err := e.Probe(c.SkipIf)
			if err != nil {
				return taps, fmt.Errorf("feature %q step %d: %w", feature, i+1, err)
			}
			if skip {
				e.log.Debug("ui skip", "feature", feature, "control", name, "because", c.SkipIf)
				continue
			}
		}

		if err := e.Tap(name); err != nil {
			return taps, fmt.Errorf("feature %q step %d/%d: %w", feature, i+1, len(steps), err)
		}
		taps++
	}
	return taps, nil
}

// Features lists the feature names in the loaded map, sorted.
func (e *Engine) Features() []string { return sortedKeys(e.ver.Features) }

// Probes lists the probe names in the loaded map, sorted.
func (e *Engine) Probes() []string { return sortedKeys(e.ver.Probes) }

// Controls lists the control names in the loaded map, sorted.
func (e *Engine) Controls() []string { return sortedKeys(e.ver.Controls) }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SelectPen forces the pen tool. Every writing session must begin with this:
// injected strokes are rendered with whatever tool is currently selected, so a
// user who left the eraser active would have the daemon silently erase instead
// of write, with no error anywhere (M0 finding).
//
// tapped reports whether any tap was actually needed: when the pen is already
// selected this is a zero-tap probe, and the caller can start writing at once.
// After a real tap, xochitl discards injected pen input for a few seconds
// (measured 2026-07-18), so the caller must wait before injecting strokes.
func (e *Engine) SelectPen() (tapped bool, err error) {
	taps, err := e.run("select_pen", nil)
	return taps > 0, err
}

// SetPenType switches to a specific pen. control is a control name such as
// "pen_marker".
func (e *Engine) SetPenType(control string) error {
	return e.Run("select_pen_type", map[string]string{"pen_type": control})
}

// SetPenSize switches stroke width; control is e.g. "size_thick". Each pen
// remembers its own width, so pick the pen type first.
func (e *Engine) SetPenSize(control string) error {
	return e.Run("select_pen_size", map[string]string{"size": control})
}

// SetPenColor switches colour; control is e.g. "color_red".
func (e *Engine) SetPenColor(control string) error {
	return e.Run("select_pen_color", map[string]string{"color": control})
}

// SetPen applies a full pen style. Empty fields are left as they are. This is
// the daemon-side primitive behind the write_text/draw tools' style parameter:
// the model declares intent, the daemon walks the UI path (plan.md 4.3).
func (e *Engine) SetPen(penType, size, color string) error {
	for _, step := range []struct {
		control string
		apply   func(string) error
	}{
		{penType, e.SetPenType},
		{size, e.SetPenSize},
		{color, e.SetPenColor},
	} {
		if step.control == "" {
			continue
		}
		if err := step.apply(step.control); err != nil {
			return err
		}
	}
	return nil
}

// CurrentTool reads which tool xochitl has selected, via the selected-state
// probes: "pen", "eraser", "select", or "" when none of the probed states
// hold (an unprobed tool like text or highlighter, or a hidden toolbar).
func (e *Engine) CurrentTool() string {
	for _, t := range []struct{ name, probe string }{
		{"pen", "pen_selected"},
		{"eraser", "eraser_selected"},
		{"select", "select_selected"},
	} {
		if _, ok := e.ver.Probes[t.probe]; !ok {
			continue
		}
		if ok, err := e.Probe(t.probe); err == nil && ok {
			return t.name
		}
	}
	return ""
}

// RestoreTool switches back to a tool previously reported by CurrentTool.
// The user should not find their pen swapped after a session (plan 4.3).
func (e *Engine) RestoreTool(name string) error {
	switch name {
	case "", "pen":
		return nil // pen is what sessions leave selected anyway
	case "eraser", "select":
		return e.Tap("tool_" + name)
	default:
		return fmt.Errorf("unknown tool %q", name)
	}
}

// Undo taps the undo button. Reserved for the daemon's own error recovery.
func (e *Engine) Undo() error { return e.Run("undo_last", nil) }

// ErasePage clears the whole page via the eraser panel's "Erase all", then
// restores the pen tool. Destructive: callers must back up the notebook first.
func (e *Engine) ErasePage() error { return e.Run("erase_page", nil) }
