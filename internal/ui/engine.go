package ui

import (
	"fmt"
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
}

// Engine executes UI features: probe the precondition, tap, verify the
// transition.
type Engine struct {
	ver *Version
	tap Tapper
	scr Screen
	log *slog.Logger
}

// New builds an engine for one firmware version's calibration.
func New(ver *Version, tap Tapper, scr Screen, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{ver: ver, tap: tap, scr: scr, log: log}
}

// Probe evaluates a named screen state once.
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

	pt, ok := p.Point()
	if !ok {
		return false, fmt.Errorf("probe %q has neither an `at` point nor `black_max`", name)
	}
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

// Tap presses a named control, checking its precondition first and its
// postcondition after.
func (e *Engine) Tap(name string) error {
	c, ok := e.ver.Controls[name]
	if !ok {
		return fmt.Errorf("unknown control %q", name)
	}
	pt, ok := c.Point()
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
	steps, ok := e.ver.Features[feature]
	if !ok {
		return fmt.Errorf("unknown feature %q", feature)
	}
	e.log.Debug("ui run", "feature", feature, "steps", len(steps))

	for i, step := range steps {
		name, conditional := strings.CutSuffix(step, "?")

		if p, ok := strings.CutPrefix(name, "<"); ok {
			key := strings.TrimSuffix(p, ">")
			v, ok := params[key]
			if !ok {
				return fmt.Errorf("feature %q step %d needs parameter %q", feature, i+1, key)
			}
			name = v
		}

		if conditional {
			c, ok := e.ver.Controls[name]
			if !ok {
				return fmt.Errorf("feature %q step %d: unknown control %q", feature, i+1, name)
			}
			if c.SkipIf == "" {
				return fmt.Errorf("feature %q step %d: %q is conditional but the map gives it no skip_if state", feature, i+1, name)
			}
			skip, err := e.Probe(c.SkipIf)
			if err != nil {
				return fmt.Errorf("feature %q step %d: %w", feature, i+1, err)
			}
			if skip {
				e.log.Debug("ui skip", "feature", feature, "control", name, "because", c.SkipIf)
				continue
			}
		}

		if err := e.Tap(name); err != nil {
			return fmt.Errorf("feature %q step %d/%d: %w", feature, i+1, len(steps), err)
		}
	}
	return nil
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
func (e *Engine) SelectPen() error { return e.Run("select_pen", nil) }

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

// Undo taps the undo button. Reserved for the daemon's own error recovery.
func (e *Engine) Undo() error { return e.Run("undo_last", nil) }

// ErasePage clears the whole page via the eraser panel's "Erase all", then
// restores the pen tool. Destructive: callers must back up the notebook first.
func (e *Engine) ErasePage() error { return e.Run("erase_page", nil) }
