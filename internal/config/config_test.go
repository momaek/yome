package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/momaek/yome/internal/inject"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Errorf("the built-in defaults do not validate: %v", err)
	}
}

// The defaults are the M0 calibration. Drifting from them silently would
// change how every stroke lands, so the measured ones are pinned here.
func TestDefaultsCarryTheM0Calibration(t *testing.T) {
	c := Default()
	if c.Inject.PenDevice != "Wacom I2C Digitizer" {
		t.Errorf("pen device = %q, want the measured evdev name", c.Inject.PenDevice)
	}
	if c.Inject.TouchDevice != "pt_mt" {
		t.Errorf("touch device = %q, want the measured evdev name", c.Inject.TouchDevice)
	}
	if c.Inject.PointDelayMs < 4 || c.Inject.PointDelayMs > 5 {
		t.Errorf("point delay = %dms, want the 4-5ms that injected cleanly", c.Inject.PointDelayMs)
	}
	if b := c.Inject.Pressure.Base; b < 3300 || b > 3600 {
		t.Errorf("base pressure = %d, want the measured 3300-3600", b)
	}
	if c.Layout.LineSpacing < 1.8 {
		t.Errorf("line spacing = %v, below the 1.8 that keeps descenders clear", c.Layout.LineSpacing)
	}
	if c.API.MaxTurns != 8 {
		t.Errorf("max turns = %d, want the agreed 8", c.API.MaxTurns)
	}
}

func TestDefaultGestureZoneIsTheInertCorner(t *testing.T) {
	r := Default().Gesture.Rect()
	// The bottom-right corner is where xochitl ignores taps (M0), which is
	// what makes a double tap there safe to use as a trigger.
	if r.Right() != 1404 || r.Bottom() != 1872 {
		t.Errorf("trigger zone %+v is not anchored to the bottom-right corner", r)
	}
	if r.W <= 0 || r.H <= 0 || r.W > 400 || r.H > 400 {
		t.Errorf("trigger zone %+v is not a small corner region", r)
	}
}

func TestLayoutArea(t *testing.T) {
	c := Default()
	a := c.Layout.Area()
	m := c.Layout.Margin
	if a.X != m.Left || a.Y != m.Top {
		t.Errorf("writable area starts at (%v,%v), want the margins (%v,%v)", a.X, a.Y, m.Left, m.Top)
	}
	if a.W != 1404-m.Left-m.Right {
		t.Errorf("writable width = %v, want the page minus its side margins", a.W)
	}
	if a.H != 1872-m.Top-m.Bottom {
		t.Errorf("writable height = %v, want the page minus its top and bottom margins", a.H)
	}
}

func TestInjectOptionsRoundTrip(t *testing.T) {
	c := Default()
	o := c.Inject.Options()
	if o.PointDelay != time.Duration(c.Inject.PointDelayMs)*time.Millisecond {
		t.Errorf("point delay = %v, want %dms", o.PointDelay, c.Inject.PointDelayMs)
	}
	if o.Pressure.Base != c.Inject.Pressure.Base {
		t.Errorf("pressure base = %d, want %d", o.Pressure.Base, c.Inject.Pressure.Base)
	}
	if o.Pressure != inject.DefaultPressure() {
		t.Errorf("the config defaults produce %+v, not the inject package's %+v", o.Pressure, inject.DefaultPressure())
	}
}

func TestLoadOverlaysOnDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	// A partial file: only what it changes.
	body := `
[api]
provider = "openai"
base_url = "http://10.0.0.2:8000/v1"
model = "qwen2-vl"

[layout]
cap_height_px = 64
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.API.Provider != "openai" || c.API.Model != "qwen2-vl" {
		t.Errorf("api = %+v, want the file's values", c.API)
	}
	if c.Layout.CapHeightPx != 64 {
		t.Errorf("cap height = %v, want the file's 64", c.Layout.CapHeightPx)
	}
	// Everything the file omitted keeps its calibrated default.
	if c.API.MaxTurns != 8 {
		t.Errorf("max turns = %d, want the default 8 to survive a partial file", c.API.MaxTurns)
	}
	if c.Inject.Pressure.Base != Default().Inject.Pressure.Base {
		t.Error("a partial config file wiped the pressure defaults")
	}
	if c.Inject.PenDevice != Default().Inject.PenDevice {
		t.Error("a partial config file wiped the device names")
	}
}

func TestLoadMissingFile(t *testing.T) {
	// The default path being absent is fine: the debug commands run
	// unconfigured. Any other path was asked for explicitly, so it must exist.
	if _, err := Load(DefaultPath); err != nil {
		t.Errorf("a missing config at the default path should fall back to defaults, got %v", err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Error("a missing config at an explicit path reported success")
	}
}

func TestLoadRejectsMalformedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(path, []byte("[api\nprovider = "), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("malformed TOML reported success")
	}
}

func TestLoadValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[api]\nprovider = \"gemini\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("an unsupported provider was accepted")
	}
	if !strings.Contains(err.Error(), "provider") {
		t.Errorf("error %q does not point at the offending field", err)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		bend func(*Config)
		want string
	}{{
		name: "unknown provider",
		bend: func(c *Config) { c.API.Provider = "gemini" },
		want: "provider",
	}, {
		name: "max turns below one",
		bend: func(c *Config) { c.API.MaxTurns = 0 },
		want: "max_turns",
	}, {
		name: "zero cap height",
		bend: func(c *Config) { c.Layout.CapHeightPx = 0 },
		want: "cap_height_px",
	}, {
		name: "margins eat the page",
		bend: func(c *Config) { c.Layout.Margin = Margin{Top: 1000, Bottom: 1000, Left: 800, Right: 800} },
		want: "writable area",
	}, {
		name: "pressure over the device maximum",
		bend: func(c *Config) { c.Inject.Pressure.Base = 99999 },
		want: "pressure.base",
	}, {
		name: "malformed gesture zone",
		bend: func(c *Config) { c.Gesture.Zone = []float64{1, 2} },
		want: "gesture.zone",
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.bend(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	c := Default()
	c.API.Provider = "gemini"
	c.API.MaxTurns = 0
	c.Layout.CapHeightPx = -1

	err := c.Validate()
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	// Fixing a config one error per run is miserable; report them together.
	for _, want := range []string{"provider", "max_turns", "cap_height_px"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q omits the %q problem", err, want)
		}
	}
}
