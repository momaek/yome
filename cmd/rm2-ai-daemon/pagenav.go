// Page navigation for the agent tools: turning pages by injected swipe,
// with frame fingerprints as the only truth about whether anything happened.
// M0 measured swipe page-turns at 10/10 reliability; the fingerprint checks
// are there for the day that stops being true.
package main

import (
	"crypto/md5"
	"fmt"
	"log/slog"
	"time"

	"github.com/momaek/yome/internal/geom"
	"github.com/momaek/yome/internal/ui"
)

// pageDir is a page-turn direction in view space.
type pageDir int

const (
	pageNext pageDir = iota // view-space left swipe
	pagePrev                // view-space right swipe
)

// Swipe geometry and timing. Endpoints sit at mid-height, well inside the
// page area, clear of the toolbar strip and edge gestures.
const (
	swipeSteps    = 30
	swipeDelay    = 5 * time.Millisecond
	swipeEdgeFrac = 0.25 // endpoints at 25% and 75% of the view width

	// pageSettle is the poll budget for a page turn to show up in the frame.
	// loadPage plus the e-ink refresh usually lands within a second.
	pageSettle = 3 * time.Second
	// framePoll is the fingerprint re-read interval while waiting.
	framePoll = 200 * time.Millisecond
)

// fingerprint hashes the physical frame. Rendering is deterministic, so the
// same page yields the same hash, and any visible change yields a new one.
func (s *session) fingerprint() ([16]byte, error) {
	fb, err := s.requireFB()
	if err != nil {
		return [16]byte{}, err
	}
	img, err := fb.Image()
	if err != nil {
		return [16]byte{}, err
	}
	return md5.Sum(img.Pix), nil
}

// waitFrameChange polls until the frame differs from old, or times out.
func (s *session) waitFrameChange(old [16]byte, timeout time.Duration) (changed bool, err error) {
	deadline := time.Now().Add(timeout)
	for {
		fp, err := s.fingerprint()
		if err != nil {
			return false, err
		}
		if fp != old {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(framePoll)
	}
}

// waitFrameStable waits until two consecutive reads agree, so a capture taken
// mid-refresh is never handed to the model.
func (s *session) waitFrameStable(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	prev, err := s.fingerprint()
	if err != nil {
		return err
	}
	for {
		time.Sleep(framePoll)
		cur, err := s.fingerprint()
		if err != nil {
			return err
		}
		if cur == prev {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("frame kept changing for %s — the screen is not settling", timeout)
		}
		prev = cur
	}
}

// turnPage swipes one page in the given view-space direction and reports
// whether the frame actually changed (it does not on the first/last page).
func (s *session) turnPage(dir pageDir) (turned bool, err error) {
	before, err := s.fingerprint()
	if err != nil {
		return false, err
	}

	w, h := float64(geom.ScreenW), float64(geom.ScreenH)
	if s.orientation() == ui.Landscape {
		w, h = geom.LandscapeW, geom.LandscapeH
	}
	from := geom.Point{X: w * (1 - swipeEdgeFrac), Y: h / 2}
	to := geom.Point{X: w * swipeEdgeFrac, Y: h / 2}
	if dir == pagePrev {
		from, to = to, from
	}
	if s.orientation() == ui.Landscape {
		from = geom.LandscapeToPortrait(from)
		to = geom.LandscapeToPortrait(to)
	}

	slog.Debug("page turn swipe", "dir", int(dir), "from", from, "to", to)
	if err := s.in.Swipe(from, to, swipeSteps, swipeDelay); err != nil {
		return false, err
	}

	changed, err := s.waitFrameChange(before, pageSettle)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	// Let the refresh finish before anyone captures or verifies ink.
	if err := s.waitFrameStable(pageSettle); err != nil {
		slog.Warn("frame not stable after page turn", "err", err)
	}
	return true, nil
}
