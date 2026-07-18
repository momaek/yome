// Package config loads the daemon's TOML configuration.
//
// Every constant calibrated in M0 lives here as a defaulted field, so the
// device can be retuned without a rebuild. Defaults are the measured values;
// a config file only needs to carry what it changes.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/momaek/yome/internal/geom"
	"github.com/momaek/yome/internal/inject"
)

// DefaultPath is where the daemon looks for its config.
//
// Note on secrets: everything on the rM2 runs as root, so file permissions
// protect nothing. The real exposure is a lost device or the USB web
// interface, not other local users.
const DefaultPath = "/home/root/.config/rm2-ai/config.toml"

// Config is the whole configuration tree.
type Config struct {
	API     API     `toml:"api"`
	Gesture Gesture `toml:"gesture"`
	Layout  Layout  `toml:"layout"`
	Inject  Inject  `toml:"inject"`
	UI      UI      `toml:"ui"`
}

// API selects and configures the model provider. The model must support
// vision: looking at the page is the daemon's only channel for reading what
// the user wrote.
type API struct {
	Provider   string `toml:"provider"` // "anthropic" or "openai"
	BaseURL    string `toml:"base_url"` // for openai-compatible self-hosted endpoints
	APIKey     string `toml:"api_key"`
	Model      string `toml:"model"`
	TimeoutSec int    `toml:"timeout_sec"`
	Retries    int    `toml:"retries"`
	MaxTurns   int    `toml:"max_turns"`
}

// Timeout is the per-request timeout.
func (a API) Timeout() time.Duration { return time.Duration(a.TimeoutSec) * time.Second }

// Gesture tunes the trigger detector.
type Gesture struct {
	// Zone is the screen-space trigger area [x0, y0, x1, y1]. The bottom-right
	// corner is inert to xochitl (M0), so a double tap there is safe.
	Zone []float64 `toml:"zone"`

	TapMaxMs    int     `toml:"tap_max_ms"`    // longer than this is not a tap
	GapMaxMs    int     `toml:"gap_max_ms"`    // max delay between the two taps
	DistMaxPx   float64 `toml:"dist_max_px"`   // max travel between the taps
	LongPressMs int     `toml:"long_press_ms"` // second tap held this long = in-place mode
	Confirm     bool    `toml:"confirm"`       // optional two-stage armed confirmation
	StatusMark  bool    `toml:"status_mark"`   // draw the corner hourglass while a session runs
}

// Rect returns the trigger zone.
func (g Gesture) Rect() geom.Rect {
	if len(g.Zone) != 4 {
		return geom.Rect{}
	}
	return geom.Rect{X: g.Zone[0], Y: g.Zone[1], W: g.Zone[2] - g.Zone[0], H: g.Zone[3] - g.Zone[1]}
}

// Layout controls typesetting.
type Layout struct {
	CapHeightPx float64 `toml:"cap_height_px"`
	LineSpacing float64 `toml:"line_spacing"` // in cap heights; <1.8 makes descenders collide
	Margin      Margin  `toml:"margin"`

	// PenType and PenSize are UI-map control names (e.g. "pen_fineliner",
	// "size_medium") forced once per session before writing. A pressure-width
	// brush like the marker turns injected strokes into scratchy wobble, so
	// legibility depends on writing with a uniform-width pen. Empty leaves
	// the user's current pen untouched.
	PenType string `toml:"pen_type"`
	PenSize string `toml:"pen_size"`
}

// Margin is the unwritable border around the page, in pixels.
type Margin struct {
	Top, Right, Bottom, Left float64
}

// Area is the writable region of a page.
func (l Layout) Area() geom.Rect {
	return geom.FullPage().Inset(l.Margin.Top, l.Margin.Right, l.Margin.Bottom, l.Margin.Left)
}

// Inject controls the feel and timing of pen injection.
type Inject struct {
	PenDevice    string   `toml:"pen_device"`
	TouchDevice  string   `toml:"touch_device"`
	PointDelayMs int      `toml:"point_delay_ms"`
	PointSpacing float64  `toml:"point_spacing_px"`
	LandingMs    int      `toml:"landing_pause_ms"`
	ToolSettleMs int      `toml:"tool_settle_ms"`
	Pressure     Pressure `toml:"pressure"`
}

// ToolSettle is how long to wait between a toolbar tap and pen injection:
// xochitl discards injected pen input for a few seconds after a touch tap on
// the toolbar (measured 2026-07-18 on 3.27.3.0 — ~3s of strokes vanished
// silently). Only applies when a tool actually had to be switched.
func (i Inject) ToolSettle() time.Duration { return time.Duration(i.ToolSettleMs) * time.Millisecond }

// Pressure is the handwriting pressure envelope. See inject.PressureEnvelope.
type Pressure struct {
	Base        int     `toml:"base"`
	Attack      float64 `toml:"attack"`
	AttackFrac  float64 `toml:"attack_frac"`
	BodyFrac    float64 `toml:"body_frac"`
	Release     float64 `toml:"release"`
	MinLengthPx float64 `toml:"min_length_px"`
}

// Options converts to the inject package's options.
func (i Inject) Options() inject.Options {
	return inject.Options{
		PenDevice:    i.PenDevice,
		TouchDevice:  i.TouchDevice,
		PointDelay:   time.Duration(i.PointDelayMs) * time.Millisecond,
		PointSpacing: i.PointSpacing,
		LandingPause: time.Duration(i.LandingMs) * time.Millisecond,
		Pressure: inject.PressureEnvelope{
			Base:        i.Pressure.Base,
			Attack:      i.Pressure.Attack,
			AttackFrac:  i.Pressure.AttackFrac,
			BodyFrac:    i.Pressure.BodyFrac,
			Release:     i.Pressure.Release,
			MinLengthPx: i.Pressure.MinLengthPx,
		},
	}
}

// UI points at an optional UI map override. When empty, the maps compiled into
// the binary are used.
type UI struct {
	MapPath  string `toml:"map_path"`
	Firmware string `toml:"firmware"` // override the detected version; for testing
}

// Default returns the configuration with every M0-calibrated value in place.
func Default() Config {
	p := inject.DefaultPressure()
	return Config{
		API: API{
			Provider:   "anthropic",
			Model:      "claude-opus-4-8",
			TimeoutSec: 60,
			Retries:    1,
			MaxTurns:   8,
		},
		Gesture: Gesture{
			Zone:        []float64{1104, 1572, geom.ScreenW, geom.ScreenH},
			TapMaxMs:    300,
			GapMaxMs:    600,
			DistMaxPx:   120,
			LongPressMs: 1500,
			Confirm:     false,
			StatusMark:  true,
		},
		Layout: Layout{
			CapHeightPx: 50,
			LineSpacing: 1.8,
			// Left clears the toolbar strip (view-left ~110px in both
			// orientations) with slack: pen strokes over toolbar icons press
			// them — a line's first character once opened the text keyboard
			// mid-write (M1 on-device finding).
			Margin: Margin{Top: 120, Right: 100, Bottom: 120, Left: 150},
			// The fineliner draws uniform-width strokes; on-device comparison
			// (2026-07-18) showed the pressure-width marker renders injected
			// writing as scratchy wobble.
			PenType: "pen_fineliner",
			PenSize: "size_medium",
		},
		Inject: Inject{
			PenDevice:    inject.PenDeviceName,
			TouchDevice:  inject.TouchDeviceName,
			PointDelayMs: 5,
			PointSpacing: 3,
			LandingMs:    15,
			ToolSettleMs: 3500,
			Pressure: Pressure{
				Base:        p.Base,
				Attack:      p.Attack,
				AttackFrac:  p.AttackFrac,
				BodyFrac:    p.BodyFrac,
				Release:     p.Release,
				MinLengthPx: p.MinLengthPx,
			},
		},
	}
}

// Load reads a config file over the defaults. A missing file at the default
// path is not an error: the CLI debug commands work fully unconfigured.
func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && path == DefaultPath {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks the values that would otherwise fail deep inside a device
// action, where the cause is much harder to see.
func (c Config) Validate() error {
	var errs []error
	if c.API.Provider != "anthropic" && c.API.Provider != "openai" {
		errs = append(errs, fmt.Errorf("api.provider must be \"anthropic\" or \"openai\", got %q", c.API.Provider))
	}
	if c.API.MaxTurns < 1 {
		errs = append(errs, fmt.Errorf("api.max_turns must be at least 1, got %d", c.API.MaxTurns))
	}
	if c.Layout.CapHeightPx <= 0 {
		errs = append(errs, fmt.Errorf("layout.cap_height_px must be positive, got %v", c.Layout.CapHeightPx))
	}
	if a := c.Layout.Area(); a.W <= 0 || a.H <= 0 {
		errs = append(errs, fmt.Errorf("layout.margin leaves no writable area (%.0fx%.0f)", a.W, a.H))
	}
	// The toolbar occupies the view-left ~110px strip and pen strokes over its
	// icons press them (a line's first character once opened the text
	// keyboard). Writing there is never right.
	if c.Layout.Margin.Left < 115 {
		errs = append(errs, fmt.Errorf("layout.margin.left = %v would put strokes over the toolbar strip (view x < 110), where the pen presses UI buttons; use at least 115", c.Layout.Margin.Left))
	}
	if c.Inject.Pressure.Base <= 0 || c.Inject.Pressure.Base > inject.WacomMaxPres {
		errs = append(errs, fmt.Errorf("inject.pressure.base must be in 1..%d, got %d", inject.WacomMaxPres, c.Inject.Pressure.Base))
	}
	if len(c.Gesture.Zone) != 4 {
		errs = append(errs, fmt.Errorf("gesture.zone must be [x0, y0, x1, y1], got %d values", len(c.Gesture.Zone)))
	}
	return errors.Join(errs...)
}
