// The M3 agent toolset. Every action tool funnels into the same write path
// (pen-tool guard + ink verification); page movement goes through pagenav's
// fingerprint checks. Tools share a toolbox so the writable area flows: each
// write advances it, new_page and erase_page reset it (plan 4.3).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/momaek/yome/internal/agent"
	"github.com/momaek/yome/internal/backup"
	"github.com/momaek/yome/internal/capture"
	"github.com/momaek/yome/internal/geom"
	"github.com/momaek/yome/internal/layout"
	"github.com/momaek/yome/internal/llm"
	"github.com/momaek/yome/internal/trigger"
)

// toolbox is the state the tools share within one session.
type toolbox struct {
	s    *session // nil when offline
	face *layout.Face
	opt  layout.TextOptions // opt.Area is the current free (writable) area
	dry  bool

	styled bool  // a pen style was applied and not restored
	cur    style // the currently applied style

	// dry-run bookkeeping for the -dry-run report
	lastText string
	lines    int
	overflow int
}

// assembleTools builds the session's tool table. The mode gates the
// destructive tool: erase_page exists only in an in-place session — a
// new-page session's model has no way to even name it (plan 4.3).
func assembleTools(tb *toolbox, mode trigger.Mode) []agent.Tool {
	tools := []agent.Tool{
		&writeTextTool{tb},
		&drawTool{tb},
	}
	if !tb.dry {
		tools = append(tools, &readPageTool{tb}, &newPageTool{tb})
		if mode == trigger.InPlace {
			tools = append(tools, &erasePageTool{tb})
		}
	}
	if tb.s != nil && tb.s.statusOn {
		for i, t := range tools {
			tools[i] = &statusTool{Tool: t, s: tb.s}
		}
	}
	return tools
}

// statusTool wraps a tool so the bottom status line tracks the session's
// phase: the tool's own label while it runs, back to "thinking" when it
// returns (the next model call is what follows). Pure decoration — results
// and errors pass through untouched.
type statusTool struct {
	agent.Tool
	s *session
}

func (t *statusTool) Run(ctx context.Context, input json.RawMessage) (agent.Result, error) {
	name := t.Tool.Def().Name
	if name == "new_page" {
		// Leaving the current page: erase the line now or it is stranded
		// there — the post-run rewrite lands on the new page.
		t.s.clearStatus()
	} else if label, ok := statusLabels[name]; ok {
		t.s.setStatus(label)
	}
	res, err := t.Tool.Run(ctx, input)
	t.s.setStatus(statusThinking)
	return res, err
}

// ---- style -----------------------------------------------------------------

// style is the model-declared pen intent; the daemon walks the UI path.
type style struct {
	Pen   string `json:"pen,omitempty"`
	Size  string `json:"size,omitempty"`
	Color string `json:"color,omitempty"`
}

// Style vocabularies, mapped onto UI-map control names. Highlighter and
// shader are deliberately absent: translucent tools render written content
// as gray smears (M1 on-device finding).
var (
	stylePens   = wordSet("ballpoint", "fineliner", "pencil", "mech_pencil", "calligraphy", "marker", "paintbrush")
	styleSizes  = wordSet("thin", "medium", "thick")
	styleColors = wordSet("black", "gray", "white", "blue", "red")
)

func wordSet(ws ...string) map[string]bool {
	m := make(map[string]bool, len(ws))
	for _, w := range ws {
		m[w] = true
	}
	return m
}

// styleSchema is the shared JSON-schema fragment for the style parameter.
const styleSchema = `{
	"type": "object",
	"description": "optional pen style; omit for the default pen",
	"properties": {
		"pen":   {"type": "string", "enum": ["ballpoint", "fineliner", "pencil", "mech_pencil", "calligraphy", "marker", "paintbrush"]},
		"size":  {"type": "string", "enum": ["thin", "medium", "thick"]},
		"color": {"type": "string", "enum": ["black", "gray", "white", "blue", "red"]}
	}
}`

// applyStyle switches the pen via the UI map when the request differs from
// what is already applied. Panel taps make xochitl drop injected pen input
// for a few seconds, so a real switch is followed by the settle wait.
func (tb *toolbox) applyStyle(st style) error {
	if st == (style{}) || st == tb.cur {
		return nil
	}
	if st.Pen != "" && !stylePens[st.Pen] {
		return fmt.Errorf("unknown pen %q", st.Pen)
	}
	if st.Size != "" && !styleSizes[st.Size] {
		return fmt.Errorf("unknown size %q", st.Size)
	}
	if st.Color != "" && !styleColors[st.Color] {
		return fmt.Errorf("unknown color %q", st.Color)
	}
	if tb.dry || tb.s == nil {
		slog.Info("dry-run: style not applied", "style", st)
		tb.cur, tb.styled = st, true
		return nil
	}
	engine, err := tb.s.requireUI()
	if err != nil {
		return fmt.Errorf("pen style needs the UI map: %w", err)
	}
	var pen, size, color string
	if st.Pen != "" {
		pen = "pen_" + st.Pen
	}
	if st.Size != "" {
		size = "size_" + st.Size
	}
	if st.Color != "" {
		color = "color_" + st.Color
	}
	if err := engine.SetPen(pen, size, color); err != nil {
		return fmt.Errorf("set pen style: %w", err)
	}
	tb.cur, tb.styled = st, true
	settle := tb.s.cfg.Inject.ToolSettle()
	slog.Info("pen style applied; waiting for xochitl to accept pen input again", "style", st, "settle", settle)
	time.Sleep(settle)
	return nil
}

// resetArea points the flowing writable area at a fresh page.
func (tb *toolbox) resetArea() {
	tb.opt.Area = tb.s.pageArea()
}

// advanceArea moves the flowing area below what was just written.
func (tb *toolbox) advanceArea(usedBottom float64) {
	a := tb.opt.Area
	if usedBottom <= a.Y {
		return
	}
	a.H -= usedBottom - a.Y
	a.Y = usedBottom
	tb.opt.Area = a
}

// box resolves a tool's optional explicit placement over the flowing area.
func (tb *toolbox) box(x, y, w, h float64) geom.Rect {
	area := tb.opt.Area
	if x > 0 {
		area.X = x
	}
	if y > 0 {
		area.Y = y
	}
	if w > 0 {
		area.W = w
	}
	if h > 0 {
		area.H = h
	}
	full := tb.pageArea()
	if area.Right() > full.Right() {
		area.W = full.Right() - area.X
	}
	if area.Bottom() > full.Bottom() {
		area.H = full.Bottom() - area.Y
	}
	return area
}

func (tb *toolbox) pageArea() geom.Rect {
	if tb.s != nil {
		return tb.s.pageArea()
	}
	// Offline: the area the toolbox was built with is all there is.
	return tb.opt.Area
}

// ---- write_text ------------------------------------------------------------

type writeTextTool struct{ tb *toolbox }

func (t *writeTextTool) Def() llm.Tool {
	return llm.Tool{
		Name:        "write_text",
		Description: "Write text onto the current page in a handwriting-style pen font. Without x/y it flows below everything already written. Supports Chinese and English; \\n makes a line break. Returns any lines that did not fit — write those after new_page.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"lines":   {"type": "string", "description": "the full text to write"},
				"x":       {"type": "number", "description": "optional left edge in page px"},
				"y":       {"type": "number", "description": "optional top edge in page px"},
				"width":   {"type": "number", "description": "optional column width in px"},
				"size_px": {"type": "number", "description": "optional cap height in px (default from config)"},
				"style":   ` + styleSchema + `
			},
			"required": ["lines"]
		}`),
	}
}

func (t *writeTextTool) Run(ctx context.Context, input json.RawMessage) (agent.Result, error) {
	tb := t.tb
	var in struct {
		Lines  string  `json:"lines"`
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Width  float64 `json:"width"`
		SizePx float64 `json:"size_px"`
		Style  style   `json:"style"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return agent.Result{}, fmt.Errorf("write_text: bad input: %v", err)
	}
	if strings.TrimSpace(in.Lines) == "" {
		return agent.Result{}, fmt.Errorf("write_text: lines is empty")
	}
	if err := tb.applyStyle(in.Style); err != nil {
		return agent.Result{}, fmt.Errorf("write_text: %v", err)
	}

	opt := tb.opt
	explicit := in.X > 0 || in.Y > 0 || in.Width > 0
	if explicit {
		opt.Area = tb.box(in.X, in.Y, in.Width, 0)
	}
	if in.SizePx > 0 {
		opt.CapHeightPx = in.SizePx
	}

	res := layout.Text(tb.face, in.Lines, opt)
	tb.lastText = in.Lines
	tb.lines += len(res.Lines)
	tb.overflow += len(res.Overflow)

	if tb.dry {
		slog.Info("dry-run: not writing", "lines", len(res.Lines), "strokes", len(res.Strokes))
	} else {
		if err := tb.s.writeStrokes(res.Strokes, true, true); err != nil {
			return agent.Result{}, err
		}
	}

	if !explicit {
		used := float64(len(res.Lines)) * opt.CapHeightPx * opt.LineSpacing
		tb.advanceArea(opt.Area.Y + used)
	}

	if len(res.Overflow) > 0 {
		return agent.Result{Text: fmt.Sprintf(
			"ok: wrote %d lines, but these %d lines did NOT fit and were not written — call new_page, then write_text them:\n%s",
			len(res.Lines), len(res.Overflow), strings.Join(res.Overflow, "\n"))}, nil
	}
	return agent.Result{Text: "ok"}, nil
}

// ---- draw ------------------------------------------------------------------

type drawTool struct{ tb *toolbox }

func (t *drawTool) Def() llm.Tool {
	return llm.Tool{
		Name:        "draw",
		Description: "Draw vector graphics as pen strokes. Give SVG path data (M/L/H/V/C/S/Q/T/Z; A becomes a line). Paths are scaled together to fit the target box, keeping aspect ratio. Without x/y the drawing goes below everything already written.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"svg_paths": {"type": "array", "items": {"type": "string"}, "description": "SVG path d attributes, one per stroke group"},
				"x":      {"type": "number", "description": "optional target box left edge in page px"},
				"y":      {"type": "number", "description": "optional target box top edge in page px"},
				"width":  {"type": "number", "description": "optional target box width in px"},
				"height": {"type": "number", "description": "optional target box height in px"},
				"style":  ` + styleSchema + `
			},
			"required": ["svg_paths"]
		}`),
	}
}

func (t *drawTool) Run(ctx context.Context, input json.RawMessage) (agent.Result, error) {
	tb := t.tb
	var in struct {
		Paths  []string `json:"svg_paths"`
		X      float64  `json:"x"`
		Y      float64  `json:"y"`
		Width  float64  `json:"width"`
		Height float64  `json:"height"`
		Style  style    `json:"style"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return agent.Result{}, fmt.Errorf("draw: bad input: %v", err)
	}
	if len(in.Paths) == 0 {
		return agent.Result{}, fmt.Errorf("draw: svg_paths is empty")
	}
	strokes, err := layout.ParsePaths(in.Paths)
	if err != nil {
		return agent.Result{}, fmt.Errorf("draw: %v", err)
	}
	if err := tb.applyStyle(in.Style); err != nil {
		return agent.Result{}, fmt.Errorf("draw: %v", err)
	}

	area := tb.box(in.X, in.Y, in.Width, in.Height)
	if area.W < 40 || area.H < 40 {
		return agent.Result{}, fmt.Errorf("draw: the target area is only %.0fx%.0f px — too small; call new_page for more room", area.W, area.H)
	}
	fitted := layout.Fit(strokes, layout.FitOptions{Area: area, Scale: 1})
	if len(fitted) == 0 {
		return agent.Result{}, fmt.Errorf("draw: the paths flattened to nothing drawable")
	}

	if tb.dry {
		slog.Info("dry-run: not drawing", "paths", len(in.Paths), "strokes", len(fitted))
	} else {
		if err := tb.s.writeStrokes(fitted, true, true); err != nil {
			return agent.Result{}, err
		}
	}

	explicit := in.X > 0 || in.Y > 0
	if !explicit {
		if b, ok := geom.Bounds(fitted); ok {
			tb.advanceArea(b.Bottom() + tb.opt.CapHeightPx/2)
		}
	}
	return agent.Result{Text: "ok"}, nil
}

// ---- read_page -------------------------------------------------------------

type readPageTool struct{ tb *toolbox }

func (t *readPageTool) Def() llm.Tool {
	return llm.Tool{
		Name:        "read_page",
		Description: "Look at the previous (offset -1) or next (offset 1) page: the page is flipped to, captured, and flipped back. Returns its screenshot. Use it when the trigger page continues from another page.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"offset": {"type": "integer", "enum": [-1, 1]}
			},
			"required": ["offset"]
		}`),
	}
}

func (t *readPageTool) Run(ctx context.Context, input json.RawMessage) (agent.Result, error) {
	tb := t.tb
	var in struct {
		Offset int `json:"offset"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return agent.Result{}, fmt.Errorf("read_page: bad input: %v", err)
	}
	if in.Offset != -1 && in.Offset != 1 {
		return agent.Result{}, fmt.Errorf("read_page: offset must be -1 or 1, got %d", in.Offset)
	}

	homeImg, err := tb.s.viewImage()
	if err != nil {
		return agent.Result{}, fmt.Errorf("read_page: %w", err)
	}
	home := maskedSum(homeImg)

	dir, back := pagePrev, pageNext
	if in.Offset == 1 {
		dir, back = pageNext, pagePrev
	}
	turned, err := tb.s.turnPage(dir)
	if err != nil {
		return agent.Result{}, fmt.Errorf("read_page: %w", err)
	}
	if !turned {
		side := "first"
		if in.Offset == 1 {
			side = "last"
		}
		return agent.Result{Text: fmt.Sprintf("this is already the %s page; there is no page at offset %d", side, in.Offset)}, nil
	}

	img, err := tb.s.viewImage()
	if err != nil {
		return agent.Result{}, fmt.Errorf("read_page: capture: %w", err)
	}
	var png bytes.Buffer
	if err := capture.WritePNG(&png, img); err != nil {
		return agent.Result{}, fmt.Errorf("read_page: %w", err)
	}

	msg := fmt.Sprintf("screenshot of the page at offset %d attached", in.Offset)
	if _, err := tb.s.turnPage(back); err != nil {
		return agent.Result{}, fmt.Errorf("read_page: captured, but could not flip back: %w", err)
	}
	if nowImg, err := tb.s.viewImage(); err == nil && maskedSum(nowImg) != home {
		// Not byte-identical — but xochitl's re-render of the same page can
		// wobble a few antialiased stroke edges (measured 8 px on device), so
		// only a substantial difference means we are on the wrong page. A
		// genuinely different page differs by thousands of pixels.
		const samePageTolerancePx = 100
		bbox, n := diffBBox(homeImg, nowImg)
		if n > samePageTolerancePx {
			// The model must not write while the page state is uncertain.
			msg += "; WARNING: after flipping back the screen does not match the original page — do NOT write; finish with a text explanation instead"
			slog.Warn("read_page: frame after return does not match the original page", "diff_px", n, "bbox", bbox)
		} else {
			slog.Debug("read_page: same page within render tolerance", "diff_px", n, "bbox", bbox)
		}
	}
	return agent.Result{
		Text:   msg,
		Images: []llm.Part{llm.ImagePart("image/png", png.Bytes())},
	}, nil
}

// ---- new_page --------------------------------------------------------------

type newPageTool struct{ tb *toolbox }

func (t *newPageTool) Def() llm.Tool {
	return llm.Tool{
		Name:        "new_page",
		Description: "Insert a fresh blank page after the current one and switch to it. The writable area resets to the full page. Use it before writing content that belongs on its own page, or to continue after write_text reported overflow.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
	}
}

func (t *newPageTool) Run(ctx context.Context, input json.RawMessage) (agent.Result, error) {
	tb := t.tb
	engine, err := tb.s.requireUI()
	if err != nil {
		return agent.Result{}, fmt.Errorf("new_page needs the UI map: %w", err)
	}

	before, err := tb.s.fingerprint()
	if err != nil {
		return agent.Result{}, fmt.Errorf("new_page: %w", err)
	}
	if err := engine.Run("add_page", nil); err != nil {
		return agent.Result{}, fmt.Errorf("new_page: %w", err)
	}
	changed, err := tb.s.waitFrameChange(before, pageSettle)
	if err != nil {
		return agent.Result{}, fmt.Errorf("new_page: %w", err)
	}
	if !changed {
		return agent.Result{}, fmt.Errorf("new_page: the screen did not change after the add-page taps — assume no page was added")
	}
	if err := tb.s.waitFrameStable(pageSettle); err != nil {
		slog.Warn("new_page: frame not stable", "err", err)
	}

	// The menu taps put xochitl in its discard-injected-pen-input window;
	// wait it out before anyone writes (M1 finding).
	settle := tb.s.cfg.Inject.ToolSettle()
	slog.Info("new page added; waiting for xochitl to accept pen input again", "settle", settle)
	time.Sleep(settle)

	tb.resetArea()
	return agent.Result{Text: "ok: now on a fresh page; the full page is writable"}, nil
}

// ---- erase_page ------------------------------------------------------------

type erasePageTool struct{ tb *toolbox }

func (t *erasePageTool) Def() llm.Tool {
	return llm.Tool{
		Name:        "erase_page",
		Description: "Erase every stroke on the current page. Destructive; the notebook is backed up on disk first. Only for rewriting the current page in place — after erasing, write the improved version with write_text/draw.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
	}
}

func (t *erasePageTool) Run(ctx context.Context, input json.RawMessage) (agent.Result, error) {
	tb := t.tb
	engine, err := tb.s.requireUI()
	if err != nil {
		return agent.Result{}, fmt.Errorf("erase_page needs the UI map: %w", err)
	}

	// Byte-level backup before anything is destroyed (plan 4.5). A backup
	// failure aborts the erase — never the other way around.
	uuid, err := backup.CurrentNotebook(backup.DefaultDataDir)
	if err != nil {
		return agent.Result{}, fmt.Errorf("erase_page: cannot identify the notebook to back up: %w", err)
	}
	dest, err := backup.Notebook(backup.DefaultDataDir, uuid, backup.DefaultBackupDir)
	if err != nil {
		return agent.Result{}, fmt.Errorf("erase_page: backup failed, not erasing: %w", err)
	}
	slog.Info("notebook backed up", "uuid", uuid, "dest", dest)

	if err := engine.ErasePage(); err != nil {
		return agent.Result{}, fmt.Errorf("erase_page: %w", err)
	}

	settle := tb.s.cfg.Inject.ToolSettle()
	slog.Info("page erased; waiting for xochitl to accept pen input again", "settle", settle)
	time.Sleep(settle)

	tb.resetArea()
	return agent.Result{Text: "ok: the page is blank; the full page is writable (backup: " + dest + ")"}, nil
}
