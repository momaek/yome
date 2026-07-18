package main

import (
	"fmt"
	"image"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/momaek/yome/internal/capture"
	"github.com/momaek/yome/internal/config"
	"github.com/momaek/yome/internal/geom"
	"github.com/momaek/yome/internal/inject"
	"github.com/momaek/yome/internal/penstate"
	"github.com/momaek/yome/internal/ui"
)

// defaultConfigPath is the on-device path; off-device it would just be noise.
func defaultConfigPath() string {
	if runtime.GOOS != "linux" {
		return "rm2-ai.toml"
	}
	return config.DefaultPath
}

// configLayout keeps the flag-override helper readable.
type configLayout = config.Layout

// loadConfigOnly loads the config without opening any device, for the dry-run
// paths that only need the layout parameters.
func loadConfigOnly(path string) (config.Config, error) { return config.Load(path) }

// session bundles the three device handles the commands need, wired together.
type session struct {
	cfg config.Config
	in  *inject.Injector
	fb  *capture.Framebuffer
	ui  *ui.Engine

	orient         ui.Orientation
	orientDetected bool

	pen     penstate.State
	penErr  error
	penRead bool
}

// recordPenState reads the user's pen type/size/color from xochitl's settings,
// once, before the session's own panel taps can overwrite them. A read failure
// is kept, not raised: it only matters if the model changes the style, and
// then the restore path reports it honestly.
func (s *session) recordPenState() {
	if s.penRead {
		return
	}
	s.penRead = true
	s.pen, s.penErr = penstate.Read(penstate.DefaultConfPath)
	if s.penErr != nil {
		slog.Debug("user's pen style unknown", "err", s.penErr)
		return
	}
	slog.Debug("user's pen style recorded", "state", s.pen)
}

// restorePenStyle puts the user's pen back after the model styled it, via the
// same panel path the model used. Best effort with honest logs: an unreadable
// conf or an unmapped pen id means the model's choice sticks, as before.
func (s *session) restorePenStyle() {
	if s.penErr != nil {
		slog.Warn("the session changed the pen style and the user's previous pen could not be read", "err", s.penErr)
		return
	}
	pen, size, color := s.pen.Controls()
	if pen == "" {
		slog.Warn("the session changed the pen style and the user's pen id has no calibrated control", "state", s.pen)
		return
	}
	if err := s.ui.SetPen(pen, size, color); err != nil {
		slog.Warn("could not restore the user's pen style", "pen", pen, "err", err)
		return
	}
	slog.Info("user's pen style restored", "pen", pen, "size", size, "color", color)
}

// resolveUIVersion loads the UI maps and picks the section matching the
// device's firmware. Everything firmware-coupled hangs off this: the frame
// buffer layout ships inside the map, so no map means no capture either.
func resolveUIVersion(cfg config.Config) (*ui.Version, string, error) {
	m, err := ui.LoadEmbedded()
	if err != nil {
		return nil, "", err
	}
	if cfg.UI.MapPath != "" {
		if m, err = ui.LoadFile(m, cfg.UI.MapPath); err != nil {
			return nil, "", err
		}
	}
	fw := cfg.UI.Firmware
	if fw == "" {
		if fw, err = ui.FirmwareVersion(); err != nil {
			return nil, "", fmt.Errorf("detect firmware: %w", err)
		}
	}
	ver, err := m.For(fw)
	if err != nil {
		return nil, fw, err
	}
	return ver, fw, nil
}

// captureSpec returns the firmware's frame layout from its UI map.
func captureSpec(cfg config.Config) (capture.Spec, error) {
	ver, fw, err := resolveUIVersion(cfg)
	if err != nil {
		return capture.Spec{}, err
	}
	if ver.Capture == nil {
		return capture.Spec{}, fmt.Errorf("the UI map for firmware %s has no [capture] section", fw)
	}
	return ver.Capture.Spec(), nil
}

// openSession loads the config and opens the input devices and the frame
// buffer. The frame layout and UI calibration both come from the firmware's
// UI map; without a matching map, capture and UI actions are disabled and
// s.fb / s.ui stay nil — commands must then degrade or refuse, never guess.
func openSession(path string) (*session, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}

	in, err := inject.Open(cfg.Inject.Options())
	if err != nil {
		return nil, err
	}
	s := &session{cfg: cfg, in: in}

	ver, fw, err := resolveUIVersion(cfg)
	if err != nil {
		slog.Warn("no UI map for this firmware — capture and UI actions disabled", "err", err)
		return s, nil
	}
	slog.Debug("ui map loaded", "firmware", fw)

	if ver.Capture == nil {
		slog.Warn("UI map has no [capture] section — capture disabled", "firmware", fw)
	} else if s.fb, err = capture.Open(ver.Capture.Spec()); err != nil {
		in.Close()
		return nil, err
	}

	if s.fb != nil {
		s.ui = ui.New(ver, in, s.fb, slog.Default())
	} else {
		slog.Warn("UI actions disabled: probes need the frame buffer")
	}
	return s, nil
}

func (s *session) Close() {
	if s.in != nil {
		s.in.Close()
	}
	if s.fb != nil {
		s.fb.Close()
	}
}

// requireUI returns the UI engine or explains why there isn't one.
func (s *session) requireUI() (*ui.Engine, error) {
	if s.ui == nil {
		return nil, fmt.Errorf("no UI map for this firmware, so UI actions are disabled; run with -debug to see why")
	}
	return s.ui, nil
}

// requireFB returns the frame buffer or explains why there isn't one.
func (s *session) requireFB() (*capture.Framebuffer, error) {
	if s.fb == nil {
		return nil, fmt.Errorf("capture is unavailable (no UI map matched this firmware); run with -debug to see why")
	}
	return s.fb, nil
}

// orientation detects how the current notebook is rotated, once per session.
// The result also points the UI engine's coordinate transforms. When detection
// is impossible (no engine) or fails (not in a notebook view), it warns and
// falls back to portrait — the write itself will still be ink-verified.
func (s *session) orientation() ui.Orientation {
	if s.orientDetected {
		return s.orient
	}
	s.orientDetected = true
	s.orient = ui.Portrait
	if s.ui == nil {
		return s.orient
	}
	o, err := s.ui.DetectOrientation()
	if err != nil {
		slog.Warn("orientation detection failed; assuming portrait", "err", err)
		return s.orient
	}
	slog.Debug("orientation detected", "orientation", o.String())
	s.orient = o
	return s.orient
}

// invalidateOrientation forces re-detection at the next orientation() call.
// Orientation is a per-notebook property (3.27 calibration): a resident
// daemon must re-detect at every session start, never cache across them.
func (s *session) invalidateOrientation() { s.orientDetected = false }

// ensureNotebookView proves the screen is a writable page by finding the
// toolbar, and refuses when there is none — the CLI write path may degrade
// to a portrait assumption (ink verification catches a bad write), but a
// gesture-triggered session must never inject into the home screen or a
// menu (2026-07-18 on-device finding: the old fallback happily tapped away
// on a book view).
//
// A missing toolbar can also just mean it is collapsed — xochitl collapses
// it after a text-tool session even inside a notebook — so before giving
// up, the toggle is tried in each orientation, using the map's calibrated
// control (its verify probe doubles as the check that the guess was right).
func (s *session) ensureNotebookView() error {
	if s.ui == nil {
		return fmt.Errorf("no UI map for this firmware: cannot tell a notebook from anything else")
	}
	detect := func() bool {
		o, err := s.ui.DetectOrientation()
		if err != nil {
			return false
		}
		s.orient, s.orientDetected = o, true
		return true
	}
	if detect() {
		return nil
	}
	for _, o := range []ui.Orientation{ui.Portrait, ui.Landscape} {
		s.ui.SetOrientation(o)
		if err := s.ui.Tap("toolbar_toggle"); err != nil {
			slog.Debug("toolbar toggle attempt failed", "assumed", o.String(), "err", err)
			continue
		}
		if detect() {
			return nil
		}
	}
	return fmt.Errorf("no toolbar found in either orientation, even after toggle taps: not on a writable page — refusing to act")
}

// pageArea returns the writable area in the current orientation's view
// coordinates: strokes are laid out in view space and rotated at write time.
func (s *session) pageArea() geom.Rect {
	m := s.cfg.Layout.Margin
	if s.orientation() == ui.Landscape {
		return geom.LandscapePage().Inset(m.Top, m.Right, m.Bottom, m.Left)
	}
	return s.cfg.Layout.Area()
}

// writeStrokes performs a full write with the safety rails M0 showed are
// needed: force the pen tool first, then confirm ink actually landed.
//
// Both matter for the same reason. Injected strokes are rendered with whatever
// tool xochitl has selected, so if the user left the eraser active, a write
// erases instead — silently, with no error from any layer.
func (s *session) writeStrokes(strokes []geom.Stroke, verify, selectPen bool) error {
	if len(strokes) == 0 {
		return fmt.Errorf("nothing to write")
	}

	// Strokes arrive in view coordinates; a landscape notebook needs them
	// rotated onto the physical frame (2026-07-18 calibration).
	if s.orientation() == ui.Landscape {
		slog.Info("landscape notebook: rotating strokes onto the physical frame")
		strokes = geom.LandscapeStrokes(strokes)
	}

	if !selectPen {
		slog.Warn("skipping the forced pen tool check (-no-select-pen)")
	} else if engine, err := s.requireUI(); err != nil {
		slog.Warn("cannot force the pen tool", "err", err)
	} else if tapped, err := engine.SelectPen(); err != nil {
		return fmt.Errorf("force pen tool: %w", err)
	} else if tapped {
		// A toolbar tap makes xochitl discard injected pen input for a few
		// seconds (measured 2026-07-18: ~3s of strokes vanished). Waiting
		// only after a real tap keeps the common already-on-pen case instant.
		settle := s.cfg.Inject.ToolSettle()
		slog.Info("tool switched; waiting for xochitl to accept pen input again", "settle", settle)
		time.Sleep(settle)
	}

	// Verification counts ink only inside the written area (with a small pad):
	// a full-frame count also swings when xochitl redraws UI — a keyboard
	// opening mid-write once produced a million-pixel "delta" (M1 finding).
	inkArea := writeBounds(strokes)

	var before int
	if verify {
		fb, err := s.requireFB()
		if err != nil {
			return fmt.Errorf("ink verification needs capture: %w (pass -no-verify to write anyway)", err)
		}
		if before, err = fb.InkCountRect(inkArea); err != nil {
			return fmt.Errorf("count ink before write: %w", err)
		}
		slog.Debug("ink before write", "px", before, "area", inkArea)
	}

	slog.Info("writing", "strokes", len(strokes))
	if err := s.in.Strokes(strokes); err != nil {
		return err
	}

	if !verify {
		return nil
	}
	// xochitl composites the frame a beat after the last event, so poll
	// rather than read once: a single immediate read once measured -276 on a
	// write that had in fact landed.
	var after int
	deadline := time.Now().Add(2 * time.Second)
	for {
		n, err := s.fb.InkCountRect(inkArea)
		if err != nil {
			return fmt.Errorf("count ink after write: %w", err)
		}
		after = n
		if after > before || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	delta := after - before
	slog.Info("ink verified", "area", inkArea, "before", before, "after", after, "delta", delta)
	if delta <= 0 {
		return fmt.Errorf("no ink appeared (ink pixels %d -> %d in %v): the strokes were injected but nothing was drawn — xochitl most likely has the eraser or another non-pen tool selected", before, after, inkArea)
	}
	return nil
}

// writeBounds is the physical-space rectangle the strokes will land in,
// padded a little for stroke width.
func writeBounds(strokes []geom.Stroke) image.Rectangle {
	b, ok := geom.Bounds(strokes)
	if !ok {
		return image.Rect(0, 0, geom.ScreenW, geom.ScreenH)
	}
	const pad = 12
	return image.Rect(int(b.X)-pad, int(b.Y)-pad, int(b.Right())+pad, int(b.Bottom())+pad)
}

// viewImage captures the frame as the user sees it: the physical portrait
// frame, rotated into landscape-view orientation when the notebook is
// rotated. This is the image a vision model must be shown.
func (s *session) viewImage() (*image.Gray, error) {
	fb, err := s.requireFB()
	if err != nil {
		return nil, err
	}
	img, err := fb.Image()
	if err != nil {
		return nil, err
	}
	if s.orientation() == ui.Landscape {
		img = capture.ToLandscapeView(img)
	}
	return img, nil
}

// freeAreaBelowInk shrinks the writable area to the space below the lowest
// user ink.
//
// Page-template dot grids are pure black but only 1-2px across (measured on
// 3.27), so darkness alone cannot tell a template from handwriting. Real
// strokes are 3px+ wide, so a row only counts as inked when it holds a
// contiguous run of dark pixels — isolated dots never form one.
func freeAreaBelowInk(img *image.Gray, area geom.Rect, gapPx float64) geom.Rect {
	const (
		minRun   = 3 // shortest dark run that counts as a stroke crossing
		minRunPx = 6 // total stroke pixels a row needs to count as content
	)
	lowest := -1.0
	x0, x1 := int(area.X), int(area.Right())
	y0, y1 := int(area.Y), int(area.Bottom())
	b := img.Bounds()
	x0, x1 = max(x0, b.Min.X), min(x1, b.Max.X)
	y0, y1 = max(y0, b.Min.Y), min(y1, b.Max.Y)
	for y := y0; y < y1; y++ {
		run, strokePx := 0, 0
		for x := x0; x < x1; x++ {
			if img.Pix[y*img.Stride+x] < capture.BlackThreshold {
				run++
				if run >= minRun {
					strokePx++
				}
				continue
			}
			run = 0
		}
		if strokePx >= minRunPx {
			lowest = float64(y)
		}
	}
	if lowest < 0 {
		return area // blank page: everything is free
	}
	top := lowest + gapPx
	return geom.Rect{X: area.X, Y: top, W: area.W, H: area.Bottom() - top}
}

// markError injects a small "×" near the page's bottom-right corner (the
// trigger zone, inert to xochitl) so the user sees that a session failed
// even without a terminal attached. Best effort: it must never mask the
// original error.
func (s *session) markError() {
	const arm = 40.0
	w, h := float64(geom.ScreenW), float64(geom.ScreenH)
	if s.orientation() == ui.Landscape {
		w, h = geom.LandscapeW, geom.LandscapeH
	}
	x1, y1 := w-60, h-60
	x0, y0 := x1-arm, y1-arm
	strokes := []geom.Stroke{
		{Points: []geom.Point{{X: x0, Y: y0}, {X: x1, Y: y1}}},
		{Points: []geom.Point{{X: x1, Y: y0}, {X: x0, Y: y1}}},
	}
	if s.orientation() == ui.Landscape {
		strokes = geom.LandscapeStrokes(strokes)
	}
	if err := s.in.Strokes(strokes); err != nil {
		slog.Warn("could not draw the error mark", "err", err)
	}
}

// readTextArg resolves -text / -file into the text to write.
func readTextArg(text, file string) (string, error) {
	switch {
	case file != "" && text != "":
		return "", fmt.Errorf("%w: give -text or -file, not both", errUsage)
	case file == "-":
		b, err := os.ReadFile("/dev/stdin")
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(b), nil
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", file, err)
		}
		return string(b), nil
	case text != "":
		// Convenience for shells: turn a literal \n into a line break.
		return replaceLiteralNewlines(text), nil
	default:
		return "", fmt.Errorf("%w: -text or -file is required", errUsage)
	}
}

func replaceLiteralNewlines(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == 'n' {
			b = append(b, '\n')
			i++
			continue
		}
		b = append(b, s[i])
	}
	return string(b)
}
