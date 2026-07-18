// Command rm2-ai-daemon is the on-device AI assistant for the reMarkable 2.
//
// M1 exposes it as a set of foreground debug commands over the write-back
// engine; the gesture-triggered daemon arrives in M3.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
)

// version is stamped at build time by the Makefile.
var version = "dev"

// command is one CLI subcommand.
type command struct {
	name  string
	usage string
	run   func(args []string) error
}

var commands []command

func init() {
	commands = []command{
		{"serve", "resident daemon: gesture-triggered AI sessions (M3)", runServe},
		{"ai", "one perceive-decide-act session from the command line", runAI},
		{"trigger", "print recognised corner gestures (calibration)", runTrigger},
		{"write-text", "typeset text and write it on the current page", runWriteText},
		{"draw-svg", "flatten an SVG's paths and draw them", runDrawSVG},
		{"capture", "save a screenshot of the current page", runCapture},
		{"ui-run", "execute a UI feature from the map", runUIRun},
		{"erase-page", "clear the current page (destructive)", runErasePage},
		{"probe", "evaluate a UI probe against the screen", runProbe},
		{"tap", "tap the touchscreen at a physical screen point", runTap},
		{"record", "dump raw events from an input device", runRecord},
		{"preview", "render an SVG's paths to a local PNG (no device)", runPreview},
		{"swipe", "drag one finger between two physical screen points", runSwipe},
		{"devices", "list input devices and their evdev names", runDevices},
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	name := os.Args[1]
	if name == "version" || name == "-version" || name == "--version" {
		fmt.Println(version)
		return
	}
	for _, c := range commands {
		if c.name == name {
			if err := c.run(os.Args[2:]); err != nil {
				slog.Error(name+" failed", "err", err)
				os.Exit(1)
			}
			return
		}
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n\n", name)
	usage()
	os.Exit(2)
}

func usage() {
	var b strings.Builder
	fmt.Fprintf(&b, "rm2-ai-daemon — on-device AI assistant for the reMarkable 2\n\nusage: rm2-ai-daemon <command> [flags]\n\ncommands:\n")
	cs := append([]command(nil), commands...)
	sort.Slice(cs, func(i, j int) bool { return cs[i].name < cs[j].name })
	for _, c := range cs {
		fmt.Fprintf(&b, "  %-12s %s\n", c.name, c.usage)
	}
	fmt.Fprintf(&b, "\nrun `rm2-ai-daemon <command> -h` for a command's flags\n")
	fmt.Fprint(os.Stderr, b.String())
}

// commonFlags are shared by every subcommand.
type commonFlags struct {
	config *string
	debug  *bool
}

func addCommon(fs *flag.FlagSet) commonFlags {
	return commonFlags{
		config: fs.String("config", defaultConfigPath(), "config file path"),
		debug:  fs.Bool("debug", false, "verbose logging to stderr"),
	}
}

// setupLogging installs a stderr handler. Timestamps are omitted because
// journald records its own, and duplicated ones make logs harder to read.
func setupLogging(debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})))
}

// parse handles the boilerplate every subcommand repeats.
func parse(fs *flag.FlagSet, cf commonFlags, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	setupLogging(*cf.debug)
	return nil
}

var errUsage = errors.New("bad usage")
