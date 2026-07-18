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

	e, err := openEraser(cfg)
	if err != nil {
		return err
	}
	defer e.Close()

	z := cfg.Gesture.Rect()
	slog.Info("serving gestures",
		"zone", fmt.Sprintf("x %.0f-%.0f y %.0f-%.0f", z.X, z.Right(), z.Y, z.Bottom()),
		"provider", cfg.API.Provider, "model", cfg.API.Model)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	for {
		select {
		case g := <-l.Gestures():
			// The session injects touch and pen events on the devices these
			// listeners read; both stay paused until the session is over.
			l.Pause()
			e.Pause()
			serveSession(cfg, client, s, g)
			e.Resume()
			l.Resume()
		case es := <-e.Strokes():
			// Capture only for now: the log is the on-device proof that the
			// Marker's eraser is seen, and the bbox is what a future consumer
			// (e.g. telling the agent which region the user erased) needs.
			slog.Info("erase",
				"bbox", fmt.Sprintf("x %.0f-%.0f y %.0f-%.0f", es.BBox.X, es.BBox.Right(), es.BBox.Y, es.BBox.Bottom()),
				"points", es.Points,
				"duration", es.End.Sub(es.Start).Round(time.Millisecond).String())
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

	// Feedback first, everything else after: the mark is the user's only
	// signal that the gesture registered, so it must not queue behind
	// probes, preflight, or — fatally — a toolbar tap, whose discard
	// window once ate it entirely (first on-device session: no visible
	// reaction for ten seconds).
	s.markBusy(g.At)

	// Orientation is a per-notebook property; the user may have switched
	// notebooks since the last session. Detect fresh every time, and refuse
	// outright when the screen is not a writable page — no further ink, just
	// a log line (an error mark would be ink somewhere wrong).
	s.invalidateOrientation()
	if err := s.ensureNotebookView(); err != nil {
		slog.Warn("gesture ignored", "err", err)
		// The busy mark is already ink; leaving it behind reads as a hang
		// (first live gesture: "it's still loading" was a stranded ⧖).
		s.clearBusyMark()
		return
	}

	if err := preflight(cfg); err != nil {
		slog.Error("preflight failed", "err", err)
		s.markError()
		return
	}

	viewImg, err := s.viewImage()
	if err != nil {
		slog.Error("capture failed", "err", err)
		s.markError()
		return
	}

	var originalTool string
	if s.ui != nil {
		originalTool = s.ui.CurrentTool()
	}

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

// markBusy draws a small single-stroke hourglass right where the gesture
// landed, by raw injection: no probes, no pen-tool forcing, no verification —
// those all cost seconds, and this mark's whole job is sub-second feedback.
// Physical coordinates from the gesture itself, so no orientation detection
// is needed and the mark appears exactly where the user tapped. With the
// eraser selected it draws nothing, which is harmless in the inert corner;
// the first real write forces the pen as always. One stroke keeps it one
// undo away (M0: one stroke = one undo step). The model may see it in the
// screenshot — the system prompt tells it to ignore the corner glyph.
func (s *session) markBusy(at geom.Point) {
	if !s.cfg.Gesture.StatusMark {
		return
	}
	const size = 36.0
	x1 := min(at.X+size/2, geom.ScreenW-12)
	y1 := min(at.Y+size/2, geom.ScreenH-12)
	x0, y0 := x1-size, y1-size
	// top-left → top-right → bottom-left → bottom-right → top-left: ⧖
	strokes := []geom.Stroke{{Points: []geom.Point{
		{X: x0, Y: y0}, {X: x1, Y: y0}, {X: x0, Y: y1}, {X: x1, Y: y1}, {X: x0, Y: y0},
	}}}
	if err := s.in.Strokes(strokes); err != nil {
		slog.Warn("could not draw the busy mark", "err", err)
		return
	}
	s.busyMark = geom.Rect{X: x0, Y: y0, W: size, H: size}
	// Injected touch is dead to xochitl for a few seconds now; the notebook
	// gate's toggle taps wait this out (same window as tool_settle_ms, just
	// mirrored — pen first, touch after).
	s.penQuietUntil = time.Now().Add(s.cfg.Inject.ToolSettle())
}

// clearBusyMark rubs out the hourglass with the synthetic eraser — raw
// injection like the mark itself, so it needs no toolbar and works whatever
// tool is selected. Called only on the refusal path: an abandoned ⧖ is
// indistinguishable from a hang. On a non-canvas screen (home, menus) both
// the mark and the erase were no-ops, so calling it unconditionally is safe.
// The gate's toggle taps may have just opened xochitl's discard window for
// pen input (tool_settle_ms), which the rubber shares — wait it out first.
func (s *session) clearBusyMark() {
	if s.busyMark.W == 0 {
		return
	}
	m := s.busyMark
	s.busyMark = geom.Rect{}
	time.Sleep(s.cfg.Inject.ToolSettle())
	// A serpentine of horizontal passes 12px apart: dense enough to cover
	// the 36px glyph even with xochitl's thinnest eraser band.
	var pts []geom.Point
	left, right := m.X-8, m.Right()+8
	for i, y := 0, m.Y-6; y <= m.Bottom()+6; i, y = i+1, y+12 {
		if i%2 == 0 {
			pts = append(pts, geom.Point{X: left, Y: y}, geom.Point{X: right, Y: y})
		} else {
			pts = append(pts, geom.Point{X: right, Y: y}, geom.Point{X: left, Y: y})
		}
	}
	if err := s.in.Erase([]geom.Stroke{{Points: pts}}); err != nil {
		slog.Warn("could not erase the busy mark", "err", err)
	}
}
