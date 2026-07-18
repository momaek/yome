package main

import (
	"flag"
	"fmt"
	"image"
	"log/slog"
	"math"
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
	noSelectPen := fs.Bool("no-select-pen", false, "debug: skip forcing the pen tool before writing")
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
	res := layout.Text(layout.DefaultFace(), body, opt)
	slog.Info("typeset", "lines", len(res.Lines), "strokes", len(res.Strokes), "overflow_lines", len(res.Overflow))

	start := time.Now()
	if err := s.writeStrokes(res.Strokes, !*noVerify, !*noSelectPen); err != nil {
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
	f := layout.DefaultFace()
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
	if err := s.writeStrokes(fitted, !*noVerify, true); err != nil {
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

	s.orientation() // point the engine's taps at the notebook's rotation

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
	s.orientation() // point the engine's taps at the notebook's rotation

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

// runTap presses the touchscreen at a physical (portrait) screen point. A raw
// escape hatch for calibration and for recovering UI states the feature paths
// cannot reach (e.g. dismissing a keyboard).
func runTap(args []string) error {
	fs := flag.NewFlagSet("tap", flag.ExitOnError)
	cf := addCommon(fs)
	x := fs.Float64("x", -1, "physical screen x (portrait 0..1403)")
	y := fs.Float64("y", -1, "physical screen y (portrait 0..1871)")
	hold := fs.Int("hold-ms", 80, "press duration; ~600 opens a tool's options panel")
	if err := parse(fs, cf, args); err != nil {
		return err
	}
	if *x < 0 || *y < 0 {
		return fmt.Errorf("%w: -x and -y are required", errUsage)
	}

	cfg, err := loadConfigOnly(*cf.config)
	if err != nil {
		return err
	}
	in, err := inject.Open(cfg.Inject.Options())
	if err != nil {
		return err
	}
	defer in.Close()
	return in.Tap(geom.Point{X: *x, Y: *y}, time.Duration(*hold)*time.Millisecond)
}

// runSwipe drags one finger between two physical screen points (page turning).
func runSwipe(args []string) error {
	fs := flag.NewFlagSet("swipe", flag.ExitOnError)
	cf := addCommon(fs)
	x0 := fs.Float64("x0", -1, "start x")
	y0 := fs.Float64("y0", -1, "start y")
	x1 := fs.Float64("x1", -1, "end x")
	y1 := fs.Float64("y1", -1, "end y")
	steps := fs.Int("steps", 30, "interpolation points")
	delay := fs.Int("delay-ms", 5, "per-point delay")
	if err := parse(fs, cf, args); err != nil {
		return err
	}
	if *x0 < 0 || *y0 < 0 || *x1 < 0 || *y1 < 0 {
		return fmt.Errorf("%w: -x0 -y0 -x1 -y1 are required", errUsage)
	}

	cfg, err := loadConfigOnly(*cf.config)
	if err != nil {
		return err
	}
	in, err := inject.Open(cfg.Inject.Options())
	if err != nil {
		return err
	}
	defer in.Close()
	return in.Swipe(geom.Point{X: *x0, Y: *y0}, geom.Point{X: *x1, Y: *y1},
		*steps, time.Duration(*delay)*time.Millisecond)
}

// runRecord dumps raw events from an input device — the calibration tool for
// recording real interactions (M3's add-page experiment records a human doing
// it, then replays the minimal sequence).
func runRecord(args []string) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	cf := addCommon(fs)
	dev := fs.String("dev", "", "device path (default: the configured touch device)")
	pen := fs.Bool("pen", false, "record the pen digitizer instead of the touchscreen")
	sec := fs.Int("sec", 5, "recording duration in seconds")
	if err := parse(fs, cf, args); err != nil {
		return err
	}

	path := *dev
	if path == "" {
		cfg, err := loadConfigOnly(*cf.config)
		if err != nil {
			return err
		}
		name := cfg.Inject.TouchDevice
		if *pen {
			name = cfg.Inject.PenDevice
		}
		if path, err = inject.FindDevice(name); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "recording %s for %ds...\n", path, *sec)
	return inject.Record(path, time.Duration(*sec)*time.Second, func(e inject.Event) {
		fmt.Println(e)
	})
}

// runPreview rasterises an SVG's paths to a local PNG for iterating on a
// drawing before injecting it. Runs anywhere; never touches the device.
func runPreview(args []string) error {
	fs := flag.NewFlagSet("preview", flag.ExitOnError)
	cf := addCommon(fs)
	file := fs.String("file", "", "SVG file to render")
	out := fs.String("out", "preview.png", "output PNG path")
	width := fs.Int("width", 800, "output width in px")
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

	img, err := rasterize(strokes, *width)
	if err != nil {
		return err
	}
	if err := capture.SavePNG(*out, img); err != nil {
		return err
	}
	fmt.Printf("%s: %d strokes -> %s (%dx%d)\n", *file, len(strokes), *out, img.Bounds().Dx(), img.Bounds().Dy())
	return nil
}

// rasterize plots stroke polylines onto a white canvas, scaled to width.
func rasterize(strokes []geom.Stroke, width int) (*image.Gray, error) {
	b, ok := geom.Bounds(strokes)
	if !ok || b.W <= 0 {
		return nil, fmt.Errorf("nothing to render")
	}
	const pad = 10.0
	scale := (float64(width) - 2*pad) / b.W
	h := int(b.H*scale + 2*pad)
	if h < 1 {
		h = 1
	}
	img := image.NewGray(image.Rect(0, 0, width, h))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	plot := func(x, y float64) {
		for dx := -1; dx <= 0; dx++ {
			for dy := -1; dy <= 0; dy++ {
				px, py := int(x)+dx, int(y)+dy
				if px >= 0 && px < width && py >= 0 && py < h {
					img.Pix[py*img.Stride+px] = 0
				}
			}
		}
	}
	for _, s := range strokes {
		for i := 1; i < len(s.Points); i++ {
			x0, y0 := pad+(s.Points[i-1].X-b.X)*scale, pad+(s.Points[i-1].Y-b.Y)*scale
			x1, y1 := pad+(s.Points[i].X-b.X)*scale, pad+(s.Points[i].Y-b.Y)*scale
			n := int(math.Hypot(x1-x0, y1-y0)/0.5) + 1
			for j := 0; j <= n; j++ {
				t := float64(j) / float64(n)
				plot(x0+(x1-x0)*t, y0+(y1-y0)*t)
			}
		}
	}
	return img, nil
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
