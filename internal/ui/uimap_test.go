package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/momaek/yome/internal/geom"
)

// The firmware the M0 calibration was measured on.
const calibratedFirmware = "3.11.2.5"

// The Qt6 firmware, recalibrated 2026-07-17/18.
const qt6Firmware = "3.27.3.0"

func TestLoadEmbedded(t *testing.T) {
	m, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, fw := range []string{calibratedFirmware, qt6Firmware} {
		if _, err := m.For(fw); err != nil {
			t.Errorf("the shipped map lost its %s section: %v", fw, err)
		}
	}
}

// Both shipped maps must carry a capture section: the frame layout rides in
// the UI map, so a map without one leaves the daemon blind on that firmware.
func TestEmbeddedMapsCarryCaptureSpecs(t *testing.T) {
	m, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for fw, v := range m.UI {
		if v.Capture == nil {
			t.Errorf("map %s has no [capture] section", fw)
			continue
		}
		if err := v.Capture.Spec().Validate(); err != nil {
			t.Errorf("map %s capture spec: %v", fw, err)
		}
	}
}

// The 3.27 map's calibrated specifics, pinned so a regressed edit is caught
// off-device: BGRA frame after the fb0-next region, landscape overrides for
// the controls xochitl re-arranges, and the orientation probes.
func TestQt6MapCalibration(t *testing.T) {
	m, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.For(qt6Firmware)
	if err != nil {
		t.Fatal(err)
	}

	spec := v.Capture.Spec()
	if spec.Locate != "next_after_fb0" || spec.Offset != 2629640 || spec.Format != "bgra8888" {
		t.Errorf("capture spec drifted from the 2026-07-17 calibration: %+v", spec)
	}

	for _, name := range []string{"orientation_portrait", "orientation_landscape"} {
		p, ok := v.Probes[name]
		if !ok {
			t.Errorf("map has no %q probe; DetectOrientation would silently assume portrait", name)
			continue
		}
		if _, ok := p.Rect(); !ok {
			t.Errorf("probe %q has no region", name)
		}
	}

	for _, name := range []string{"page_overview", "tag", "menu_more", "menu_add_page", "menu_set_orientation"} {
		if _, ok := v.LandscapeOverrides[name]; !ok {
			t.Errorf("landscape override for %q is missing (formula coords land off the re-arranged toolbar)", name)
		}
	}

	// The selected-tool probes must be region probes: a single pixel inside an
	// icon reads dark whether or not the tool is selected.
	for _, name := range []string{"pen_selected", "eraser_selected"} {
		p := v.Probes[name]
		if _, ok := p.Rect(); !ok || p.Expect != "dark_block" {
			t.Errorf("probe %q must be a dark_block region probe, got %+v", name, p)
		}
	}
}

func TestForRefusesUnknownFirmware(t *testing.T) {
	m, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	// Coordinates from another firmware could tap anything, so an unmapped
	// version must fail rather than guess.
	_, err = m.For("9.9.9.9")
	if err == nil {
		t.Fatal("an unmapped firmware was accepted")
	}
	if !strings.Contains(err.Error(), calibratedFirmware) {
		t.Errorf("error %q does not list the versions we do have", err)
	}
}

// TestEmbeddedMapIsSelfConsistent walks the shipped map the way the engine
// will: every name a control or feature references must resolve. A typo here
// would otherwise surface as a mid-sequence failure on the device, with the UI
// left half-open.
func TestEmbeddedMapIsSelfConsistent(t *testing.T) {
	m, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for fw, v := range m.UI {
		t.Run(fw, func(t *testing.T) {
			for name, c := range v.Controls {
				if _, ok := c.Point(); !ok {
					t.Errorf("control %q has no usable tap point: %v", name, c.Tap)
				}
				for what, probe := range map[string]string{"requires": c.Requires, "verify": c.Verify, "skip_if": c.SkipIf} {
					if probe == "" {
						continue
					}
					if _, ok := v.Probes[probe]; !ok {
						t.Errorf("control %q %s an undefined probe %q", name, what, probe)
					}
				}
			}

			for name, p := range v.Probes {
				pt, hasPoint := p.Point()
				r, hasRegion := p.Rect()
				switch {
				case p.BlackMax != nil:
				case hasRegion:
					if p.Expect != "dark_block" {
						t.Errorf("region probe %q expects %q, want dark_block", name, p.Expect)
					}
					if p.BlockRatio() <= 0 || p.BlockRatio() >= 1 {
						t.Errorf("probe %q has an unusable ratio %v", name, p.BlockRatio())
					}
					full := geom.FullPage()
					if !full.Contains(geom.Point{X: float64(r.Min.X), Y: float64(r.Min.Y)}) ||
						!full.Contains(geom.Point{X: float64(r.Max.X - 1), Y: float64(r.Max.Y - 1)}) {
						t.Errorf("probe %q region %v reaches off the screen", name, r)
					}
				case hasPoint:
					if p.Expect != "dark" && p.Expect != "light" {
						t.Errorf("probe %q expects %q, want dark or light", name, p.Expect)
					}
					if !geom.FullPage().Contains(pt) {
						t.Errorf("probe %q samples %+v, off the screen", name, pt)
					}
				default:
					t.Errorf("probe %q has no `at`, `region`, or `black_max`", name)
				}
			}

			for name, c := range v.LandscapeOverrides {
				pt, ok := c.Point()
				if !ok {
					t.Errorf("landscape override %q has no usable tap point: %v", name, c.Tap)
					continue
				}
				if !geom.FullPage().Contains(pt) {
					t.Errorf("landscape override %q taps %+v, off the screen", name, pt)
				}
				// Overrides replace calibrated controls; an override for a
				// control that does not exist is a typo.
				if _, ok := v.Controls[name]; !ok {
					t.Errorf("landscape override %q has no portrait control to override", name)
				}
			}

			if v.Capture != nil {
				if err := v.Capture.Spec().Validate(); err != nil {
					t.Errorf("capture section does not validate: %v", err)
				}
			}

			for feature, steps := range v.Features {
				for _, step := range steps {
					name, conditional := strings.CutSuffix(step, "?")
					if strings.HasPrefix(name, "<") {
						continue // a parameter, resolved at run time
					}
					c, ok := v.Controls[name]
					if !ok {
						t.Errorf("feature %q references an undefined control %q", feature, name)
						continue
					}
					// A "?" step needs a skip_if state, or the engine cannot
					// decide whether to skip it.
					if conditional && c.SkipIf == "" {
						t.Errorf("feature %q uses %q conditionally, but it has no skip_if", feature, name)
					}
				}
			}
		})
	}
}

func TestEmbeddedMapControlsAreOnScreen(t *testing.T) {
	m, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.For(calibratedFirmware)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range v.Controls {
		pt, _ := c.Point()
		if !geom.FullPage().Contains(pt) {
			t.Errorf("control %q taps %+v, off the %dx%d screen", name, pt, geom.ScreenW, geom.ScreenH)
		}
	}
}

// The features the daemon calls by name must exist, or the code paths that
// depend on them fail only once they reach a device.
func TestEmbeddedMapHasTheFeaturesTheDaemonUses(t *testing.T) {
	m, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.For(calibratedFirmware)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"select_pen", "select_pen_type", "select_pen_size", "select_pen_color", "undo_last", "erase_page"} {
		if _, ok := v.Features[f]; !ok {
			t.Errorf("the map has no %q feature", f)
		}
	}
}

func TestEmbeddedMapTimingsMatchTheCalibration(t *testing.T) {
	m, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.For(calibratedFirmware)
	if err != nil {
		t.Fatal(err)
	}
	meta := v.Meta
	// Measured in M0: an options panel needs ~200ms; 60ms does nothing.
	if meta.TapLong() < 200*time.Millisecond {
		t.Errorf("tap_long_ms = %v, below the 200ms an options panel needs", meta.TapLong())
	}
	if meta.Tap() < 60*time.Millisecond {
		t.Errorf("tap_ms = %v, below the 60ms a button needs", meta.Tap())
	}
	// The gap floor keeps adjacent taps from registering as a double tap.
	if meta.TapGapMin() <= 0 {
		t.Error("tap_gap_min_ms must be positive")
	}
	if meta.ProbePoll() <= 0 || meta.ProbeTimeout() <= meta.ProbePoll() {
		t.Errorf("probe polling is misconfigured: poll=%v timeout=%v", meta.ProbePoll(), meta.ProbeTimeout())
	}
}

// erase_page is the one destructive path, so its shape is pinned: it must end
// by restoring the pen, or the next injected stroke would silently erase.
func TestErasePageRestoresThePenTool(t *testing.T) {
	m, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.For(calibratedFirmware)
	if err != nil {
		t.Fatal(err)
	}
	steps := v.Features["erase_page"]
	var sawEraseAll, sawPenAfter bool
	for _, s := range steps {
		if s == "erase_all" {
			sawEraseAll = true
		}
		if sawEraseAll && s == "tool_pen" {
			sawPenAfter = true
		}
	}
	if !sawEraseAll {
		t.Error("erase_page never taps erase_all")
	}
	if !sawPenAfter {
		t.Errorf("erase_page leaves the eraser selected: %v", steps)
	}
}

func TestLoadFileOverridesAVersion(t *testing.T) {
	base, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "override.toml")
	custom := `
[ui."9.9.9.9".meta]
tap_ms = 99
[ui."9.9.9.9".controls]
toolbar_toggle = { tap = [1, 2] }
`
	if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := LoadFile(base, path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.For("9.9.9.9")
	if err != nil {
		t.Fatalf("the override was not merged in: %v", err)
	}
	if v.Meta.TapMs != 99 {
		t.Errorf("tap_ms = %d, want the override's 99", v.Meta.TapMs)
	}
	// Merging must not drop the shipped calibration.
	if _, err := m.For(calibratedFirmware); err != nil {
		t.Errorf("the override displaced the embedded map: %v", err)
	}
}

func TestLoadFileErrors(t *testing.T) {
	base := &Map{UI: map[string]*Version{}}
	if _, err := LoadFile(base, filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Error("a missing ui map file reported success")
	}

	bad := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(bad, []byte("this is not = = toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(base, bad); err == nil {
		t.Error("a malformed ui map reported success")
	}
}
