// Page navigation for the agent tools: turning pages by injected swipe,
// with frame fingerprints as the only truth about whether anything happened.
// M0 measured swipe page-turns at 10/10 reliability; the fingerprint checks
// are there for the day that stops being true.
package main

import (
	"crypto/md5"
	"fmt"
	"image"
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

// footerMaskPx is the view-bottom strip excluded from fingerprints. xochitl
// shows a transient "Page X of Y" indicator there after every page action
// (fading seconds later), which made exact fingerprints mismatch pages that
// are in fact identical (2026-07-18 on-device finding). Masking it also
// keeps the first/last-page check honest: on a refused turn the indicator
// still appears, and without the mask that alone reads as "page changed".
const footerMaskPx = 100

// fingerprint hashes the view-space frame minus the footer strip. Rendering
// is deterministic, so the same page yields the same hash, and any content
// change yields a new one.
func (s *session) fingerprint() ([16]byte, error) {
	img, err := s.viewImage()
	if err != nil {
		return [16]byte{}, err
	}
	return maskedSum(img), nil
}

func maskedSum(img *image.Gray) [16]byte {
	b := img.Bounds()
	cut := b.Max.Y - footerMaskPx
	if cut < b.Min.Y {
		cut = b.Min.Y
	}
	return md5.Sum(img.Pix[:cut*img.Stride])
}

// diffBBox reports where two same-size frames differ (footer excluded):
// the forensic tool behind "the page does not match" warnings.
func diffBBox(a, b *image.Gray) (bbox image.Rectangle, n int) {
	if a.Bounds() != b.Bounds() {
		return a.Bounds(), -1
	}
	h := a.Bounds().Dy() - footerMaskPx
	w := a.Bounds().Dx()
	minX, minY, maxX, maxY := w, h, -1, -1
	for y := 0; y < h; y++ {
		ra, rb := a.Pix[y*a.Stride:y*a.Stride+w], b.Pix[y*b.Stride:y*b.Stride+w]
		for x := 0; x < w; x++ {
			if ra[x] != rb[x] {
				n++
				minX, minY = min(minX, x), min(minY, y)
				maxX, maxY = max(maxX, x), max(maxY, y)
			}
		}
	}
	return image.Rect(minX, minY, maxX+1, maxY+1), n
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
	// A swipe is injected touch: if a status line was just inked, xochitl
	// would silently drop it and the unchanged frame would masquerade as
	// "already on the first/last page" — a wrong answer handed to the model.
	s.touchSettle()
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
