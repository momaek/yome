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

// runTrigger prints recognised gestures until interrupted — the calibration
// tool for the gesture thresholds and zone.
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

	z := cfg.Gesture.Rect()
	fmt.Fprintf(os.Stderr, "listening; zone x %.0f-%.0f y %.0f-%.0f — double-tap = new-page, hold the second tap = in-place (Ctrl-C to stop)\n",
		z.X, z.Right(), z.Y, z.Bottom())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	for {
		select {
		case g := <-l.Gestures():
			fmt.Printf("%s  gesture %-9s at (%.0f,%.0f)\n",
				time.Now().Format("15:04:05.000"), g.Mode, g.At.X, g.At.Y)
		case <-sig:
			return nil
		}
	}
}
