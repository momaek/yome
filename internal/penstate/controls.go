// Mapping from xochitl's numeric tool state to UI-map control names.
package penstate

// penControls maps xochitl pen tool ids to pen-panel controls. The ids are
// the long-established .rm lines vocabulary (each classic tool has a v1 and
// a v2 id); id 15 = ballpoint was byte-verified against a captured conf.
// Newer tools without a verified id (shader) are deliberately absent:
// restoring a guessed-wrong pen is worse than an honest log line.
var penControls = map[int]string{
	0: "pen_paintbrush", 12: "pen_paintbrush",
	1: "pen_pencil", 13: "pen_pencil",
	2: "pen_ballpoint", 15: "pen_ballpoint",
	3: "pen_marker", 16: "pen_marker",
	4: "pen_fineliner", 17: "pen_fineliner",
	5: "pen_highlighter", 18: "pen_highlighter",
	7: "pen_mech_pencil", 14: "pen_mech_pencil",
	21: "pen_calligraphy",
}

// colorControls maps xochitl color ids to the color-row controls the 3.27
// map has calibrated. Yellow (3), green (4) and pink (5) exist on the device
// but not in the map, so they stay unmapped and are skipped on restore.
var colorControls = map[int]string{
	0: "color_black",
	1: "color_gray",
	2: "color_white",
	6: "color_blue",
	7: "color_red",
}

// Controls translates the state into SetPen control names. Unknown or
// missing fields come back empty (SetPen skips empty fields). When the pen
// itself cannot be mapped everything is empty: sizes are remembered per pen
// (M1 calibration note), so tapping a size with the wrong pen selected would
// corrupt that pen's memory instead of restoring the user's.
func (s State) Controls() (pen, size, color string) {
	pen = penControls[s.Pen]
	if pen == "" {
		return "", "", ""
	}
	switch {
	case s.PenSize == 0: // key absent
	case s.PenSize < 1.5:
		size = "size_thin"
	case s.PenSize < 2.5:
		size = "size_medium"
	default:
		size = "size_thick"
	}
	color = colorControls[s.PenColor]
	return pen, size, color
}
