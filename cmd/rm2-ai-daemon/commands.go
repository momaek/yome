package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/momaek/yome/internal/capture"
	"github.com/momaek/yome/internal/geom"
	"github.com/momaek/yome/internal/inject"
	"github.com/momaek/yome/internal/layout"
)

func runWriteText(args []string) error {
	fs := flag.NewFlagSet("write-text", flag.ExitOnError)
	cf := addCommon(fs)
	text := fs.String("text", "", "text to write (\\n is a line break)")
	file := fs.String("file", "", "read the text from a file, or - for stdin")
	size := fs.Float64("size", 0, "cap height in px (default: config layout.cap_height_px)")
	spacing := fs.Float64("spacing", 0, "line spacing in cap heights (default: config)")
	dry := fs.Bool("dry-run", false, "typeset and report, but do not touch the device")
	noVerify := fs.Bool("no-verify", false, "skip the post-write ink check")
	if err := parse(fs, cf, args); err != nil {
		return err
	}

	body, err := readTextArg(*text, *file)
	if err != nil {
		return err
	}

	if *dry {
		return dryRunText(*cf.config, body, *size, *spacing)
	}

	s, err := openSession(*cf.config)
	if err != nil {
		return err
	}
	defer s.Close()

	opt := textOptions(s.cfg.Layout, *size, *spacing)
	opt.Area = s.pageArea() // landscape notebooks get the rotated writable area
	res := layout.Text(layout.Futural(), body, opt)
	slog.Info("typeset", "lines", len(res.Lines), "strokes", len(res.Strokes), "overflow_lines", len(res.Overflow))

	start := time.Now()
	if err := s.writeStrokes(res.Strokes, !*noVerify); err != nil {
		return err
	}
	slog.Info("write complete", "elapsed", time.Since(start).Round(time.Millisecond))

	if len(res.Overflow) > 0 {
		// M1 has no page turning; report rather than silently truncate.
		slog.Warn("text did not fit on one page", "dropped_lines", len(res.Overflow))
		fmt.Fprintf(os.Stderr, "overflow:\n%s\n", strings.Join(res.Overflow, "\n"))
	}
	return nil
}

// dryRunText typesets off-device, for checking wrapping and page budget.
func dryRunText(configPath, body string, size, spacing float64) error {
	cfg, err := loadConfigOnly(configPath)
	if err != nil {
		return err
	}
	opt := textOptions(cfg.Layout, size, spacing)
	f := layout.Futural()
	res := layout.Text(f, body, opt)

	fmt.Printf("area      %.0fx%.0f at (%.0f,%.0f)\n", opt.Area.W, opt.Area.H, opt.Area.X, opt.Area.Y)
	fmt.Printf("cap %.0fpx  spacing %.2f  budget ~%d chars/page\n", opt.CapHeightPx, opt.LineSpacing, layout.PageBudget(f, opt))
	fmt.Printf("lines %d  strokes %d  overflow %d\n\n", len(res.Lines), len(res.Strokes), len(res.Overflow))
	for _, l := range res.Lines {
		fmt.Printf("| %s\n", l)
	}
	for _, l := range res.Overflow {
		fmt.Printf("~ %s\n", l)
	}
	return nil
}

func runDrawSVG(args []string) error {
	fs := flag.NewFlagSet("draw-svg", flag.ExitOnError)
	cf := addCommon(fs)
	file := fs.String("file", "", "SVG file to draw")
	x := fs.Float64("x", 0, "target box left edge in px (default: layout margin)")
	y := fs.Float64("y", 0, "target box top edge in px (default: layout margin)")
	w := fs.Float64("width", 0, "target box width in px (default: writable width)")
	h := fs.Float64("height", 0, "target box height in px (default: writable height)")
	scale := fs.Float64("scale", 1, "fraction of the box to occupy (0..1)")
	dry := fs.Bool("dry-run", false, "parse and report, but do not touch the device")
	noVerify := fs.Bool("no-verify", false, "skip the post-write ink check")
	if err := parse(fs, cf, args); err != nil {
		return err
	}
	if *file == "" {
		return fmt.Errorf("%w: -file is required", errUsage)
	}

	svg, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("read %s: %w", *file, err)
	}
	ds := layout.ExtractSVGPaths(string(svg))
	if len(ds) == 0 {
		return fmt.Errorf("%s has no <path d=\"...\"> elements", *file)
	}
	strokes, err := layout.ParsePaths(ds)
	if err != nil {
		return fmt.Errorf("%s: %w", *file, err)
	}

	// fitInto applies the flag overrides over the writable area.
	fitInto := func(area geom.Rect) []geom.Stroke {
		if *w > 0 {
			area.W = *w
		}
		if *h > 0 {
			area.H = *h
		}
		if *x > 0 {
			area.X = *x
		}
		if *y > 0 {
			area.Y = *y
		}
		return layout.Fit(strokes, layout.FitOptions{Area: area, Scale: *scale})
	}

	if *dry {
		cfg, err := loadConfigOnly(*cf.config)
		if err != nil {
			return err
		}
		fitted := fitInto(cfg.Layout.Area())
		b, _ := geom.Bounds(fitted)
		fmt.Printf("paths %d  strokes %d  bounds %.0fx%.0f at (%.0f,%.0f)\n", len(ds), len(fitted), b.W, b.H, b.X, b.Y)
		return nil
	}

	s, err := openSession(*cf.config)
	if err != nil {
		return err
	}
	defer s.Close()

	fitted := fitInto(s.pageArea()) // landscape notebooks get the rotated area
	slog.Info("svg parsed", "paths", len(ds), "strokes", len(fitted))

	start := time.Now()
	if err := s.writeStrokes(fitted, !*noVerify); err != nil {
		return err
	}
	slog.Info("draw complete", "elapsed", time.Since(start).Round(time.Millisecond))
	return nil
}

func runCapture(args []string) error {
	fs := flag.NewFlagSet("capture", flag.ExitOnError)
	cf := addCommon(fs)
	out := fs.String("out", "capture.png", "output PNG path")
	if err := parse(fs, cf, args); err != nil {
		return err
	}

	cfg, err := loadConfigOnly(*cf.config)
	if err != nil {
		return err
	}
	spec, err := captureSpec(cfg)
	if err != nil {
		return err
	}
	fb, err := capture.Open(spec)
	if err != nil {
		return err
	}
	defer fb.Close()
	slog.Debug("framebuffer located", "pid", fb.PID, "base", fmt.Sprintf("%#x", fb.Base), "format", spec.Format)

	img, err := fb.Image()
	if err != nil {
		return err
	}
	if err := capture.SavePNG(*out, img); err != nil {
		return err
	}
	fmt.Printf("%s  %dx%d  ink=%d px\n", *out, img.Bounds().Dx(), img.Bounds().Dy(), capture.CountInk(img))
	return nil
}

func runUIRun(args []string) error {
	fs := flag.NewFlagSet("ui-run", flag.ExitOnError)
	cf := addCommon(fs)
	feature := fs.String("feature", "", "feature to run, e.g. select_pen")
	params := fs.String("params", "", "comma-separated key=value feature parameters")
	list := fs.Bool("list", false, "list the features in the loaded map")
	if err := parse(fs, cf, args); err != nil {
		return err
	}

	s, err := openSession(*cf.config)
	if err != nil {
		return err
	}
	defer s.Close()
	engine, err := s.requireUI()
	if err != nil {
		return err
	}

	if *list {
		for _, name := range engine.Features() {
			fmt.Println(name)
		}
		return nil
	}
	if *feature == "" {
		return fmt.Errorf("%w: -feature is required (or -list)", errUsage)
	}

	p, err := parseParams(*params)
	if err != nil {
		return err
	}
	if err := engine.Run(*feature, p); err != nil {
		return err
	}
	slog.Info("feature done", "feature", *feature)
	return nil
}

func parseParams(s string) (map[string]string, error) {
	if s == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("%w: params must be key=value, got %q", errUsage, kv)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

func runErasePage(args []string) error {
	fs := flag.NewFlagSet("erase-page", flag.ExitOnError)
	cf := addCommon(fs)
	yes := fs.Bool("yes", false, "confirm: this clears the current page")
	if err := parse(fs, cf, args); err != nil {
		return err
	}
	if !*yes {
		return fmt.Errorf("%w: erase-page wipes the current page; pass -yes to confirm", errUsage)
	}

	s, err := openSession(*cf.config)
	if err != nil {
		return err
	}
	defer s.Close()
	engine, err := s.requireUI()
	if err != nil {
		return err
	}

	before, err := s.fb.InkCount()
	if err != nil {
		return fmt.Errorf("count ink before erase: %w", err)
	}
	if err := engine.ErasePage(); err != nil {
		return err
	}
	after, err := s.fb.InkCount()
	if err != nil {
		return fmt.Errorf("count ink after erase: %w", err)
	}
	slog.Info("erase complete", "ink_before", before, "ink_after", after)
	if after >= before && before > 0 {
		return fmt.Errorf("page does not look erased (ink pixels %d -> %d)", before, after)
	}
	return nil
}

func runProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	cf := addCommon(fs)
	name := fs.String("name", "", "probe to evaluate")
	wait := fs.Bool("wait", false, "poll until the probe holds or the map's timeout expires")
	list := fs.Bool("list", false, "list the probes in the loaded map")
	if err := parse(fs, cf, args); err != nil {
		return err
	}

	s, err := openSession(*cf.config)
	if err != nil {
		return err
	}
	defer s.Close()
	engine, err := s.requireUI()
	if err != nil {
		return err
	}

	if *list {
		for _, n := range engine.Probes() {
			fmt.Println(n)
		}
		return nil
	}
	if *name == "" {
		return fmt.Errorf("%w: -name is required (or -list)", errUsage)
	}

	if *wait {
		if err := engine.WaitProbe(*name); err != nil {
			return err
		}
		fmt.Printf("%s: true\n", *name)
		return nil
	}
	ok, err := engine.Probe(*name)
	if err != nil {
		return err
	}
	fmt.Printf("%s: %v\n", *name, ok)
	if !ok {
		os.Exit(1)
	}
	return nil
}

func runDevices(args []string) error {
	fs := flag.NewFlagSet("devices", flag.ExitOnError)
	cf := addCommon(fs)
	if err := parse(fs, cf, args); err != nil {
		return err
	}

	devs, err := inject.ListDevices()
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(devs))
	for p := range devs {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		fmt.Printf("%-22s %q\n", p, devs[p])
	}
	return nil
}

// textOptions applies the flag overrides over the configured layout.
func textOptions(l configLayout, size, spacing float64) layout.TextOptions {
	opt := layout.TextOptions{
		CapHeightPx: l.CapHeightPx,
		LineSpacing: l.LineSpacing,
		Area:        l.Area(),
	}
	if size > 0 {
		opt.CapHeightPx = size
	}
	if spacing > 0 {
		opt.LineSpacing = spacing
	}
	return opt
}
