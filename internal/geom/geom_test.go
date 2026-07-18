package geom

import "testing"

func TestBounds(t *testing.T) {
	if _, ok := Bounds(nil); ok {
		t.Error("Bounds of nothing should report ok=false")
	}
	b, ok := Bounds([]Stroke{
		{Points: []Point{{1, 2}, {5, 9}}},
		{Points: []Point{{-3, 4}}},
	})
	if !ok {
		t.Fatal("Bounds reported ok=false for real points")
	}
	if want := (Rect{X: -3, Y: 2, W: 8, H: 7}); b != want {
		t.Errorf("Bounds = %+v, want %+v", b, want)
	}
}

func TestStrokeLength(t *testing.T) {
	s := Stroke{Points: []Point{{0, 0}, {3, 4}, {3, 14}}}
	if got := s.Length(); got != 15 {
		t.Errorf("Length = %v, want 15", got)
	}
}

func TestTransform(t *testing.T) {
	in := []Stroke{{Points: []Point{{1, 1}, {2, 3}}}}
	got := Transform(in, 2, 10, 20)
	want := []Point{{12, 22}, {14, 26}}
	for i, p := range got[0].Points {
		if p != want[i] {
			t.Errorf("point %d = %+v, want %+v", i, p, want[i])
		}
	}
	if in[0].Points[0] != (Point{1, 1}) {
		t.Error("Transform mutated its input")
	}
}

func TestInset(t *testing.T) {
	got := Rect{0, 0, 100, 200}.Inset(10, 20, 30, 40)
	if want := (Rect{X: 40, Y: 10, W: 40, H: 160}); got != want {
		t.Errorf("Inset = %+v, want %+v", got, want)
	}
}

func TestClip(t *testing.T) {
	box := Rect{0, 0, 10, 10}

	tests := []struct {
		name string
		in   Stroke
		want []Stroke
	}{{
		name: "fully inside is untouched",
		in:   Stroke{Points: []Point{{1, 1}, {9, 9}}},
		want: []Stroke{{Points: []Point{{1, 1}, {9, 9}}}},
	}, {
		name: "fully outside is dropped",
		in:   Stroke{Points: []Point{{20, 20}, {30, 30}}},
		want: nil,
	}, {
		name: "leaving the box is cut at the edge",
		in:   Stroke{Points: []Point{{5, 5}, {15, 5}}},
		want: []Stroke{{Points: []Point{{5, 5}, {10, 5}}}},
	}, {
		name: "entering the box is cut at the edge",
		in:   Stroke{Points: []Point{{-5, 5}, {5, 5}}},
		want: []Stroke{{Points: []Point{{0, 5}, {5, 5}}}},
	}, {
		name: "leaving and re-entering splits into two strokes",
		in:   Stroke{Points: []Point{{5, 5}, {5, 15}, {8, 15}, {8, 5}}},
		want: []Stroke{
			{Points: []Point{{5, 5}, {5, 10}}},
			{Points: []Point{{8, 10}, {8, 5}}},
		},
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Clip([]Stroke{tc.in}, box)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d strokes, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if len(got[i].Points) != len(tc.want[i].Points) {
					t.Fatalf("stroke %d: got %d points, want %d: %+v", i, len(got[i].Points), len(tc.want[i].Points), got[i].Points)
				}
				for j, p := range got[i].Points {
					if p != tc.want[i].Points[j] {
						t.Errorf("stroke %d point %d = %+v, want %+v", i, j, p, tc.want[i].Points[j])
					}
				}
			}
		})
	}
}

func TestClipKeepsEverythingInsideTheBox(t *testing.T) {
	box := Rect{0, 0, 10, 10}
	in := []Stroke{{Points: []Point{{-5, -5}, {5, 5}, {20, 5}, {5, 20}, {5, 5}}}}
	for _, s := range Clip(in, box) {
		for _, p := range s.Points {
			if !box.Contains(p) {
				t.Errorf("clipped point %+v escaped the box", p)
			}
		}
	}
}

func TestLandscapeToPortrait(t *testing.T) {
	tests := []struct {
		name string
		view Point
		phys Point
	}{
		// The landscape view's origin (top-left when holding the device
		// sideways) is the portrait frame's bottom-left corner.
		{"view origin", Point{0, 0}, Point{0, ScreenH - 1}},
		{"view top-right", Point{LandscapeW - 1, 0}, Point{0, 0}},
		{"view bottom-left", Point{0, LandscapeH - 1}, Point{LandscapeH - 1, ScreenH - 1}},
		// Calibrated landmark: toolbar main column phys = (view_y, ~1815)
		// for controls at portrait/view x=55 (ui-map-3.27.3.0 landscape note).
		{"toolbar landmark", Point{55, 167}, Point{167, 1816}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := LandscapeToPortrait(tc.view); got != tc.phys {
				t.Errorf("LandscapeToPortrait(%v) = %v, want %v", tc.view, got, tc.phys)
			}
			if back := PortraitToLandscape(tc.phys); back != tc.view {
				t.Errorf("PortraitToLandscape(%v) = %v, want %v back", tc.phys, back, tc.view)
			}
		})
	}
}

func TestLandscapeStrokesStayOnScreen(t *testing.T) {
	// Any point inside the landscape view must land inside the portrait frame.
	s := []Stroke{{Points: []Point{{0, 0}, {LandscapeW - 1, LandscapeH - 1}, {900, 700}}}}
	page := FullPage()
	for _, st := range LandscapeStrokes(s) {
		for _, p := range st.Points {
			if !page.Contains(p) {
				t.Errorf("landscape point mapped off the physical screen: %v", p)
			}
		}
	}
}
