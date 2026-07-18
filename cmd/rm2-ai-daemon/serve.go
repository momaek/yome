// The serve command: the resident daemon form (T3.8). Listen for corner
// gestures, run one agent session per gesture, keep serving through failures.
// systemd restarts us on a crash; a session error only marks the page.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/momaek/yome/internal/config"
	"github.com/momaek/yome/internal/geom"
	"github.com/momaek/yome/internal/llm"
	"github.com/momaek/yome/internal/trigger"
	"github.com/momaek/yome/internal/ui"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cf := addCommon(fs)
	if err := parse(fs, cf, args); err != nil {
		return err
	}

	cfg, err := config.Load(*cf.config)
	if err != nil {
		return err
	}
	client, err := llm.New(llm.Config{
		Provider: cfg.API.Provider,
		BaseURL:  cfg.API.BaseURL,
		APIKey:   cfg.API.APIKey,
		Model:    cfg.API.Model,
		Timeout:  cfg.API.Timeout(),
		Retries:  cfg.API.Retries,
	})
	if err != nil {
		return err
	}
	if cfg.API.Provider == "anthropic" && cfg.API.APIKey == "" {
		return fmt.Errorf("api.api_key is empty; set it in %s before enabling the daemon", *cf.config)
	}

	s, err := openSession(*cf.config)
	if err != nil {
		return err
	}
	defer s.Close()

	l, err := openListener(cfg)
	if err != nil {
		return err
	}
	defer l.Close()

	z := cfg.Gesture.Rect()
	slog.Info("serving gestures",
		"zone", fmt.Sprintf("x %.0f-%.0f y %.0f-%.0f", z.X, z.Right(), z.Y, z.Bottom()),
		"provider", cfg.API.Provider, "model", cfg.API.Model)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	for {
		select {
		case g := <-l.Gestures():
			// The session injects touch events on the device this listener
			// reads; recognition stays paused until the session is over.
			l.Pause()
			serveSession(cfg, client, s, g)
			l.Resume()
		case got := <-sig:
			slog.Info("shutting down", "signal", got.String())
			return nil
		}
	}
}

// serveSession runs one gesture-triggered agent session, converting every
// failure into a corner mark and a log line — the daemon must keep serving.
func serveSession(cfg config.Config, client llm.Client, s *session, g trigger.Gesture) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("session panicked", "panic", r)
			s.markError()
		}
	}()

	slog.Info("gesture", "mode", g.Mode.String(), "at", fmt.Sprintf("(%.0f,%.0f)", g.At.X, g.At.Y))

	// Orientation is a per-notebook property; the user may have switched
	// notebooks since the last session. Detect fresh every time.
	s.invalidateOrientation()

	if err := preflight(cfg); err != nil {
		slog.Error("preflight failed", "err", err)
		s.markError()
		return
	}

	// Capture before the busy mark so the model never sees the mark.
	viewImg, err := s.viewImage()
	if err != nil {
		slog.Error("capture failed", "err", err)
		s.markError()
		return
	}

	// Session etiquette starts here: the busy mark forces the pen tool, so
	// the user's tool must be recorded before it, not after.
	var originalTool string
	if s.ui != nil {
		originalTool = s.ui.CurrentTool()
	}

	s.markBusy()

	start := time.Now()
	_, _, err = runAgentSession(cfg, client, s, viewImg, s.pageArea(), g.Mode, false, cfg.API.MaxTurns, "")
	if err != nil {
		slog.Error("session failed", "err", err)
		// runAgentSession already marked the error.
	} else {
		slog.Info("session served", "elapsed", time.Since(start).Round(time.Second))
	}

	if s.ui != nil && originalTool != "" {
		if err := s.ui.RestoreTool(originalTool); err != nil {
			slog.Warn("could not restore the user's tool", "tool", originalTool, "err", err)
		}
	}
}

// markBusy draws a small single-stroke hourglass near the trigger corner:
// visible feedback that the gesture registered and minutes of pen work are
// coming. One stroke keeps it one undo away (M0: one stroke = one undo step).
// Best effort — a session must not die for its status mark.
func (s *session) markBusy() {
	if !s.cfg.Gesture.StatusMark {
		return
	}
	const size = 36.0
	w, h := float64(geom.ScreenW), float64(geom.ScreenH)
	if s.orientation() == ui.Landscape {
		w, h = geom.LandscapeW, geom.LandscapeH
	}
	x1, y1 := w-60, h-60
	x0, y0 := x1-size, y1-size
	// top-left → top-right → bottom-left → bottom-right → top-left: ⧖
	strokes := []geom.Stroke{{Points: []geom.Point{
		{X: x0, Y: y0}, {X: x1, Y: y0}, {X: x0, Y: y1}, {X: x1, Y: y1}, {X: x0, Y: y0},
	}}}
	if s.orientation() == ui.Landscape {
		strokes = geom.LandscapeStrokes(strokes)
	}
	if err := s.writeStrokes(strokes, false, true); err != nil {
		slog.Warn("could not draw the busy mark", "err", err)
	}
}
