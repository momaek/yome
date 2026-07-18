package main

import (
	"testing"

	"github.com/momaek/yome/internal/geom"
	"github.com/momaek/yome/internal/layout"
	"github.com/momaek/yome/internal/trigger"
)

func toolNames(tb *toolbox, mode trigger.Mode) []string {
	var names []string
	for _, t := range assembleTools(tb, mode) {
		names = append(names, t.Def().Name)
	}
	return names
}

// The erase_page gate is the safety mechanism of the in-place mode: a
// new-page session's model must not even have the tool in its table.
func TestAssembleToolsGating(t *testing.T) {
	device := &toolbox{}
	cases := []struct {
		name string
		tb   *toolbox
		mode trigger.Mode
		want []string
	}{
		{"new-page", device, trigger.NewPage, []string{"write_text", "draw", "read_page", "new_page"}},
		{"in-place", device, trigger.InPlace, []string{"write_text", "draw", "read_page", "new_page", "erase_page"}},
		{"dry-run", &toolbox{dry: true}, trigger.InPlace, []string{"write_text", "draw"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toolNames(tc.tb, tc.mode)
			if len(got) != len(tc.want) {
				t.Fatalf("tools = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("tools = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestAdvanceAreaFlows(t *testing.T) {
	tb := &toolbox{opt: layout.TextOptions{Area: geom.Rect{X: 150, Y: 200, W: 1000, H: 1500}}}
	tb.advanceArea(500)
	a := tb.opt.Area
	if a.Y != 500 || a.H != 1200 {
		t.Fatalf("area after advance = %+v", a)
	}
	// Advancing backwards must be a no-op, never grow the area.
	tb.advanceArea(400)
	if tb.opt.Area.Y != 500 {
		t.Fatalf("advance moved backwards: %+v", tb.opt.Area)
	}
}

func TestBoxClampsToPage(t *testing.T) {
	// Offline toolbox: the page is the area it was built with.
	tb := &toolbox{opt: layout.TextOptions{Area: geom.Rect{X: 100, Y: 100, W: 1200, H: 1600}}}
	b := tb.box(1000, 1500, 900, 900) // asks past the right and bottom edges
	if b.Right() > 1300.01 || b.Bottom() > 1700.01 {
		t.Fatalf("box exceeds the page: %+v", b)
	}
	if b.X != 1000 || b.Y != 1500 {
		t.Fatalf("box ignored the requested origin: %+v", b)
	}
}
