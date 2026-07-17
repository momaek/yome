// Package ui drives xochitl's own interface: switching tools, undoing, erasing
// a page. It works from a per-firmware map of control coordinates plus pixel
// probes that confirm each state transition, so the daemon never blind-taps
// into an unknown UI.
package ui

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/momaek/yome/assets"
	"github.com/momaek/yome/internal/geom"
)

// Map is a UI map file: one section per firmware version.
type Map struct {
	UI map[string]*Version `toml:"ui"`
}

// Version is the calibration for one firmware version.
type Version struct {
	Meta     Meta                `toml:"meta"`
	Probes   map[string]Probe    `toml:"probes"`
	Controls map[string]Control  `toml:"controls"`
	Features map[string][]string `toml:"features"`
}

// Meta holds the timing constants for a firmware version.
type Meta struct {
	TapMs          int `toml:"tap_ms"`         // ordinary button press
	TapLongMs      int `toml:"tap_long_ms"`    // hold needed to open an options panel
	TapGapMinMs    int `toml:"tap_gap_min_ms"` // hard floor between taps
	ProbePollMs    int `toml:"probe_poll_ms"`
	ProbeTimeoutMs int `toml:"probe_timeout_ms"`
}

// Tap durations and polling intervals as durations.
func (m Meta) Tap() time.Duration          { return time.Duration(m.TapMs) * time.Millisecond }
func (m Meta) TapLong() time.Duration      { return time.Duration(m.TapLongMs) * time.Millisecond }
func (m Meta) TapGapMin() time.Duration    { return time.Duration(m.TapGapMinMs) * time.Millisecond }
func (m Meta) ProbePoll() time.Duration    { return time.Duration(m.ProbePollMs) * time.Millisecond }
func (m Meta) ProbeTimeout() time.Duration { return time.Duration(m.ProbeTimeoutMs) * time.Millisecond }

// Probe is a named screen state, checked either by sampling one pixel or by
// counting ink over the whole frame.
type Probe struct {
	// At is a screen-space [x, y] sample point, paired with Expect.
	At     []int  `toml:"at"`
	Expect string `toml:"expect"` // "dark" or "light"

	// BlackMax asserts the whole frame holds at most this many ink pixels.
	BlackMax *int `toml:"black_max"`
}

// Point returns the probe's sample point.
func (p Probe) Point() (geom.Point, bool) {
	if len(p.At) != 2 {
		return geom.Point{}, false
	}
	return geom.Point{X: float64(p.At[0]), Y: float64(p.At[1])}, true
}

// Control is a tappable UI element.
type Control struct {
	// Tap is the screen-space [x, y] press point.
	Tap []int `toml:"tap"`
	// Hold is "long" for controls that need the panel-opening hold.
	Hold string `toml:"hold"`

	// Requires names a probe that must hold before tapping. If it does not,
	// the UI is not where we think it is and the action aborts.
	Requires string `toml:"requires"`
	// Verify names a probe polled after tapping to confirm the transition.
	Verify string `toml:"verify"`
	// SkipIf names a probe that, when true, makes a "control?" step a no-op.
	// This is what lets a feature open the toolbar only when it is closed.
	SkipIf string `toml:"skip_if"`
}

// Point returns the control's tap point.
func (c Control) Point() (geom.Point, bool) {
	if len(c.Tap) != 2 {
		return geom.Point{}, false
	}
	return geom.Point{X: float64(c.Tap[0]), Y: float64(c.Tap[1])}, true
}

// LoadEmbedded parses the UI maps shipped with the binary.
func LoadEmbedded() (*Map, error) {
	entries, err := assets.UIMaps.ReadDir("ui")
	if err != nil {
		return nil, fmt.Errorf("read embedded ui maps: %w", err)
	}
	m := &Map{UI: map[string]*Version{}}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		b, err := assets.UIMaps.ReadFile(path.Join("ui", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read embedded ui map %s: %w", e.Name(), err)
		}
		var one Map
		if err := toml.Unmarshal(b, &one); err != nil {
			return nil, fmt.Errorf("parse embedded ui map %s: %w", e.Name(), err)
		}
		for ver, v := range one.UI {
			m.UI[ver] = v
		}
	}
	return m, nil
}

// LoadFile parses a UI map from disk and merges it over base, replacing whole
// version sections. This is how a user recalibrates without a rebuild.
func LoadFile(base *Map, path string) (*Map, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ui map %s: %w", path, err)
	}
	var one Map
	if err := toml.Unmarshal(b, &one); err != nil {
		return nil, fmt.Errorf("parse ui map %s: %w", path, err)
	}
	out := &Map{UI: map[string]*Version{}}
	for k, v := range base.UI {
		out.UI[k] = v
	}
	for k, v := range one.UI {
		out.UI[k] = v
	}
	return out, nil
}

// For returns the calibration for a firmware version. An unknown version is an
// error rather than a guess: tapping coordinates from a different firmware
// could hit anything.
func (m *Map) For(version string) (*Version, error) {
	if v, ok := m.UI[version]; ok {
		return v, nil
	}
	known := make([]string, 0, len(m.UI))
	for k := range m.UI {
		known = append(known, k)
	}
	sort.Strings(known)
	return nil, fmt.Errorf("no UI map for firmware %s (calibrated: %s); UI actions are disabled until it is mapped",
		version, strings.Join(known, ", "))
}

// FirmwareVersion reads the device's firmware version.
func FirmwareVersion() (string, error) {
	const conf = "/usr/share/remarkable/update.conf"
	b, err := os.ReadFile(conf)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", conf, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "REMARKABLE_RELEASE_VERSION="); ok {
			return strings.TrimSpace(v), nil
		}
	}
	return "", fmt.Errorf("%s has no REMARKABLE_RELEASE_VERSION line", conf)
}
