// The session status line: one short line of small handwriting at the very
// bottom of the page, rewritten as the agent moves between phases, so the
// user watching the tablet knows the difference between "thinking", "writing"
// and "hung" without a terminal (the busy hourglass only says "started").
//
// Everything here is best-effort raw injection, deliberately outside the
// safety rails: no pen forcing (a toolbar tap would cost tool_settle_ms per
// update), no ink verification. If the user's tool is an eraser the line is
// invisible; that degrades feedback, never correctness. What IS guaranteed:
//
//   - the strip lives inside pagenav's footer fingerprint mask (bottom
//     footerMaskPx of the view), so status ink never pollutes page-turn
//     checks or the first/last-page heuristic;
//   - it sits below the writable page area (layout margins), so it cannot
//     collide with content or ink verification;
//   - x stays right of the toolbar strip (pen strokes over icons press them,
//     M1) — and every status write opens the post-pen touch-rejection
//     window, which all injected touch now waits out (touchSettle).
//
// Updates erase the previous line with the synthetic rubber (no UI needed)
// and ink the new one. The line is erased outright when the session ends.
package main

import (
	"log/slog"

	"github.com/momaek/yome/internal/geom"
	"github.com/momaek/yome/internal/layout"
	"github.com/momaek/yome/internal/ui"
)

const (
	statusCapPx = 20.0  // small but still legible handwriting
	statusLeft  = 150.0 // clear of the toolbar strip (config floor is 115)
	// statusBaseGap is how far above the view bottom the cap top sits. Deep
	// enough inside the footer mask (100px) that Han rise stays masked too.
	statusBaseGap = 60.0
)

// statusLabels are the per-tool phase labels; the between-tools state is
// statusThinking. Short on purpose: every glyph is strokes on e-ink.
// new_page has no label: it moves to a fresh page, and a label inked before
// the move would be stranded on the old page forever (2026-07-18 on-device —
// the erase always lands on the page the daemon is on now). The wrapper
// clears the line before the tool runs instead.
var statusLabels = map[string]string{
	"write_text": "写入中...",
	"draw":       "画图中...",
	"read_page":  "读页中...",
	"erase_page": "擦除中...",
}

const statusThinking = "思考中..."

// setStatus replaces the bottom status line. Same-text updates are free.
func (s *session) setStatus(text string) {
	if !s.statusOn || s.in == nil {
		return
	}
	if text == s.statusText {
		return
	}
	s.eraseStatusInk()

	viewW, viewH := float64(geom.ScreenW), float64(geom.ScreenH)
	if s.orientation() == ui.Landscape {
		viewW, viewH = geom.LandscapeW, geom.LandscapeH
	}
	strokes := statusStrokes(layout.DefaultFace(), text, viewW, viewH)
	if len(strokes) == 0 {
		return
	}
	if s.orientation() == ui.Landscape {
		strokes = geom.LandscapeStrokes(strokes)
	}
	// A toolbar/panel tap may have just opened the pen-discard window (e.g.
	// the gate's toggle taps right before the first "thinking" line).
	s.penSettle()
	if err := s.in.Strokes(strokes); err != nil {
		slog.Warn("could not ink the status line", "err", err)
		return
	}
	if b, ok := geom.Bounds(strokes); ok {
		s.statusBox = b
	}
	s.statusText = text
	s.penQuiet()
}

// statusStrokes renders the line in view space: cap top at viewH −
// statusBaseGap, left edge clear of the toolbar strip, truncated before the
// right edge. Pure geometry, unit-tested against the footer-mask and
// toolbar-strip invariants.
func statusStrokes(face *layout.Face, text string, viewW, viewH float64) []geom.Stroke {
	var strokes []geom.Stroke
	x, y := statusLeft, viewH-statusBaseGap
	for _, r := range text {
		gl, adv := face.RenderRune(r, geom.Point{X: x, Y: y}, statusCapPx)
		strokes = append(strokes, gl...)
		x += adv
		if x > viewW-statusLeft {
			break // never crowd the right edge (or the gesture corner)
		}
	}
	return strokes
}

// clearStatus removes the line entirely (session end, before error marks).
func (s *session) clearStatus() {
	if s.in == nil {
		return
	}
	s.eraseStatusInk()
}

// eraseStatusInk rubs out the previously inked line, if any. The rubber is
// pen-device input, so it waits out any pen-discard window a recent tap
// opened (penSettle) — usually already expired, costing nothing.
func (s *session) eraseStatusInk() {
	if s.statusBox.W == 0 {
		return
	}
	m := s.statusBox
	s.statusBox = geom.Rect{}
	s.statusText = ""
	s.penSettle()
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
		slog.Warn("could not erase the status line", "err", err)
	}
	s.penQuiet()
}
