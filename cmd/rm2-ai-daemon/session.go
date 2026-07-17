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
}

// openSession loads the config and opens the input devices and the frame
// buffer. The UI engine is only built when a UI map matches the firmware; s.ui
// is nil otherwise, and UI actions must then be skipped rather than guessed.
func openSession(path string) (*session, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}

	in, err := inject.Open(cfg.Inject.Options())
	if err != nil {
		return nil, err
	}
	fb, err := capture.Open()
	if err != nil {
		in.Close()
		return nil, err
	}
	s := &session{cfg: cfg, in: in, fb: fb}

	engine, err := openUI(cfg, in, fb)
	if err != nil {
		slog.Warn("UI actions unavailable", "err", err)
	} else {
		s.ui = engine
	}
	return s, nil
}

func openUI(cfg config.Config, in *inject.Injector, fb *capture.Framebuffer) (*ui.Engine, error) {
	m, err := ui.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	if cfg.UI.MapPath != "" {
		if m, err = ui.LoadFile(m, cfg.UI.MapPath); err != nil {
			return nil, err
		}
	}
	fw := cfg.UI.Firmware
	if fw == "" {
		if fw, err = ui.FirmwareVersion(); err != nil {
			return nil, fmt.Errorf("detect firmware: %w", err)
		}
	}
	ver, err := m.For(fw)
	if err != nil {
		return nil, err
	}
	slog.Debug("ui map loaded", "firmware", fw)
	return ui.New(ver, in, fb, slog.Default()), nil
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

	if engine, err := s.requireUI(); err != nil {
		slog.Warn("cannot force the pen tool", "err", err)
	} else if err := engine.SelectPen(); err != nil {
		return fmt.Errorf("force pen tool: %w", err)
	}

	var before int
	if verify {
		var err error
		if before, err = s.fb.InkCount(); err != nil {
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
