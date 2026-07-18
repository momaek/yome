package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/momaek/yome/internal/config"
	"github.com/momaek/yome/internal/inject"
	"github.com/momaek/yome/internal/trigger"
)

// triggerOptions converts the config's gesture section for the detector.
func triggerOptions(g config.Gesture) trigger.Options {
	return trigger.Options{
		Zone:      g.Rect(),
		TapMax:    time.Duration(g.TapMaxMs) * time.Millisecond,
		GapMax:    time.Duration(g.GapMaxMs) * time.Millisecond,
		DistMax:   g.DistMaxPx,
		LongPress: time.Duration(g.LongPressMs) * time.Millisecond,
	}
}

// openListener locates the touch device and starts gesture recognition.
func openListener(cfg config.Config) (*trigger.Listener, error) {
	path, err := inject.FindDevice(cfg.Inject.TouchDevice)
	if err != nil {
		return nil, fmt.Errorf("locate touch device: %w", err)
	}
	return trigger.Listen(path, triggerOptions(cfg.Gesture), nil)
}

// openEraser locates the pen digitizer and starts eraser-stroke capture.
func openEraser(cfg config.Config) (*trigger.EraserListener, error) {
	path, err := inject.FindDevice(cfg.Inject.PenDevice)
	if err != nil {
		return nil, fmt.Errorf("locate pen device: %w", err)
	}
	return trigger.ListenEraser(path, nil)
}

// runTrigger prints recognised gestures and eraser strokes until interrupted —
// the calibration tool for the gesture thresholds, the zone, and the eraser.
func runTrigger(args []string) error {
	fs := flag.NewFlagSet("trigger", flag.ExitOnError)
	cf := addCommon(fs)
	if err := parse(fs, cf, args); err != nil {
		return err
	}

	cfg, err := loadConfigOnly(*cf.config)
	if err != nil {
		return err
	}
	l, err := openListener(cfg)
	if err != nil {
		return err
	}
	defer l.Close()

	e, err := openEraser(cfg)
	if err != nil {
		return err
	}
	defer e.Close()

	z := cfg.Gesture.Rect()
	fmt.Fprintf(os.Stderr, "listening; zone x %.0f-%.0f y %.0f-%.0f — double-tap = new-page, hold the second tap = in-place; pen-eraser strokes are printed too (Ctrl-C to stop)\n",
		z.X, z.Right(), z.Y, z.Bottom())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	for {
		select {
		case g := <-l.Gestures():
			fmt.Printf("%s  gesture %-9s at (%.0f,%.0f)\n",
				time.Now().Format("15:04:05.000"), g.Mode, g.At.X, g.At.Y)
		case s := <-e.Strokes():
			fmt.Printf("%s  erase     x %.0f-%.0f y %.0f-%.0f (%d pts, %s)\n",
				time.Now().Format("15:04:05.000"),
				s.BBox.X, s.BBox.Right(), s.BBox.Y, s.BBox.Bottom(),
				s.Points, s.End.Sub(s.Start).Round(time.Millisecond))
		case <-sig:
			return nil
		}
	}
}
