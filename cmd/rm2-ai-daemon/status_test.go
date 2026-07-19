package main

import (
	"testing"

	"github.com/momaek/yome/internal/geom"
	"github.com/momaek/yome/internal/layout"
)

// The status line's whole safety story is geometric: it must stay inside
// pagenav's footer fingerprint mask (bottom 100px of the view) and clear of
// the toolbar strip. If either invariant breaks, status ink starts polluting
// page-turn checks or pressing toolbar icons.
func TestStatusStrokesStayInsideTheFooterMask(t *testing.T) {
	face := layout.DefaultFace()
	views := []struct {
		name string
		w, h float64
	}{
		{"portrait", geom.ScreenW, geom.ScreenH},
		{"landscape", geom.LandscapeW, geom.LandscapeH},
	}
	labels := make([]string, 0, len(statusLabels)+1)
	labels = append(labels, statusThinking)
	for _, l := range statusLabels {
		labels = append(labels, l)
	}

	const footerMask = 100 // keep in sync with pagenav's footerMaskPx
	for _, v := range views {
		for _, text := range labels {
			strokes := statusStrokes(face, text, v.w, v.h)
			if len(strokes) == 0 {
				t.Fatalf("%s %q: no strokes rendered", v.name, text)
			}
			b, ok := geom.Bounds(strokes)
			if !ok {
				t.Fatalf("%s %q: no bounds", v.name, text)
			}
			if b.Y < v.h-footerMask {
				t.Errorf("%s %q: ink rises to y=%.0f, above the footer mask start %.0f", v.name, text, b.Y, v.h-footerMask)
			}
			if b.Bottom() > v.h {
				t.Errorf("%s %q: ink reaches y=%.0f, past the view bottom %.0f", v.name, text, b.Bottom(), v.h)
			}
			if b.X < 115 {
				t.Errorf("%s %q: ink starts at x=%.0f, inside the toolbar strip (<115)", v.name, text, b.X)
			}
			if b.Right() > v.w-115 {
				t.Errorf("%s %q: ink ends at x=%.0f, crowding the right edge of %.0f", v.name, text, b.Right(), v.w)
			}
		}
	}
}

// A status update on a session without the flag (CLI runs) or without
// devices (offline) must be a silent no-op, never a panic.
func TestStatusIsANoOpWhenDisabled(t *testing.T) {
	s := &session{}
	s.setStatus(statusThinking) // statusOn=false, in=nil
	s.clearStatus()
	if s.statusText != "" || s.statusBox.W != 0 {
		t.Fatalf("disabled status mutated state: %+v", s)
	}
}
