package main

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"

	"github.com/momaek/yome/internal/capture"
	"github.com/momaek/yome/internal/config"
	"github.com/momaek/yome/internal/geom"
	"github.com/momaek/yome/internal/inject"
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
func (s *session) writeStrokes(strokes []geom.Stroke, verify bool) error {
	if len(strokes) == 0 {
		return fmt.Errorf("nothing to write")
	}

	// Strokes arrive in view coordinates; a landscape notebook needs them
	// rotated onto the physical frame (2026-07-18 calibration).
	if s.orientation() == ui.Landscape {
		slog.Info("landscape notebook: rotating strokes onto the physical frame")
		strokes = geom.LandscapeStrokes(strokes)
	}

	if engine, err := s.requireUI(); err != nil {
		slog.Warn("cannot force the pen tool", "err", err)
	} else if err := engine.SelectPen(); err != nil {
		return fmt.Errorf("force pen tool: %w", err)
	}

	var before int
	if verify {
		fb, err := s.requireFB()
		if err != nil {
			return fmt.Errorf("ink verification needs capture: %w (pass -no-verify to write anyway)", err)
		}
		if before, err = fb.InkCount(); err != nil {
			return fmt.Errorf("count ink before write: %w", err)
		}
		slog.Debug("ink before write", "px", before)
	}

	slog.Info("writing", "strokes", len(strokes))
	if err := s.in.Strokes(strokes); err != nil {
		return err
	}

	if !verify {
		return nil
	}
	after, err := s.fb.InkCount()
	if err != nil {
		return fmt.Errorf("count ink after write: %w", err)
	}
	delta := after - before
	slog.Info("ink verified", "before", before, "after", after, "delta", delta)
	if delta <= 0 {
		return fmt.Errorf("no ink appeared (ink pixels %d -> %d): the strokes were injected but nothing was drawn — xochitl most likely has the eraser or another non-pen tool selected", before, after)
	}
	return nil
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
