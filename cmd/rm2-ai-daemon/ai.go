// The ai command: M2's perceive-and-write session. Screenshot in, model
// decides, write_text puts the reply back on the page — the maxTurns=1
// degenerate form of the full agent (plan 5, M2).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/momaek/yome/internal/agent"
	"github.com/momaek/yome/internal/capture"
	"github.com/momaek/yome/internal/config"
	"github.com/momaek/yome/internal/geom"
	"github.com/momaek/yome/internal/layout"
	"github.com/momaek/yome/internal/llm"
)

func runAI(args []string) error {
	fs := flag.NewFlagSet("ai", flag.ExitOnError)
	cf := addCommon(fs)
	imagePath := fs.String("image", "", "offline replay: use this PNG instead of the device (implies -dry-run)")
	instruction := fs.String("instruction", "", "extra instruction to send alongside the screenshot")
	dry := fs.Bool("dry-run", false, "call the model but print the reply instead of writing it")
	maxTurns := fs.Int("max-turns", 1, "tool-execution rounds (M2 runs single-shot)")
	if err := parse(fs, cf, args); err != nil {
		return err
	}

	cfg, err := loadConfigOnly(*cf.config)
	if err != nil {
		return err
	}
	client, err := llm.New(llm.Config{
		Provider: cfg.API.Provider,
		BaseURL:  cfg.API.BaseURL,
		APIKey:   cfg.API.APIKey,
		Model:    cfg.API.Model,
		Timeout:  cfg.API.Timeout(),
		Retries:  cfg.API.Retries,
	})
	if err != nil {
		return err
	}
	if cfg.API.Provider == "anthropic" && cfg.API.APIKey == "" {
		return fmt.Errorf("api.api_key is empty; set it in %s", *cf.config)
	}

	offline := *imagePath != ""
	dryRun := *dry || offline

	// Gather the page image and the writable space.
	var (
		viewImg *image.Gray
		s       *session
		area    geom.Rect
	)
	if offline {
		viewImg, err = loadGrayPNG(*imagePath)
		if err != nil {
			return err
		}
		m := cfg.Layout.Margin
		page := geom.Rect{W: float64(viewImg.Bounds().Dx()), H: float64(viewImg.Bounds().Dy())}
		area = page.Inset(m.Top, m.Right, m.Bottom, m.Left)
	} else {
		// The model call is pointless if the API is unreachable; check before
		// touching the page (T2.6).
		if err := preflight(cfg); err != nil {
			return err
		}
		if s, err = openSession(*cf.config); err != nil {
			return err
		}
		defer s.Close()
		if viewImg, err = s.viewImage(); err != nil {
			return err
		}
		area = s.pageArea()
	}

	face := layout.DefaultFace()
	capH := cfg.Layout.CapHeightPx
	free := freeAreaBelowInk(viewImg, area, capH)
	opt := layout.TextOptions{CapHeightPx: capH, LineSpacing: cfg.Layout.LineSpacing, Area: free}
	if lines := (free.H) / (capH * layout.MinLineSpacing); lines < 2 {
		err := fmt.Errorf("only %.0fpx of free space below the ink — not enough to write a reply", free.H)
		if s != nil {
			s.markError()
		}
		return err
	}

	system := agent.SystemPrompt(agent.PromptParams{
		PageW: viewImg.Bounds().Dx(), PageH: viewImg.Bounds().Dy(),
		FreeW: int(free.W), FreeH: int(free.H),
		LatinBudget: layout.PageBudget(face, opt),
		HanBudget:   layout.HanPageBudget(face, opt),
	})
	slog.Debug("free area", "rect", free, "latin_budget", layout.PageBudget(face, opt), "han_budget", layout.HanPageBudget(face, opt))

	var png bytes.Buffer
	if err := capture.WritePNG(&png, viewImg); err != nil {
		return err
	}

	tool := &writeTextTool{face: face, opt: opt, s: s, dry: dryRun}
	initial := []llm.Part{llm.ImagePart("image/png", png.Bytes())}
	msg := *instruction
	if msg == "" {
		msg = "The user triggered you on this page. Follow your instructions."
	}
	initial = append(initial, llm.TextPart(msg))

	loop := &agent.Loop{
		Client: client, Tools: []agent.Tool{tool},
		System: system, MaxTurns: *maxTurns,
	}

	// Session etiquette (T2.5): remember the user's tool, put it back after.
	var originalTool string
	if s != nil && s.ui != nil {
		s.orientation()
		originalTool = s.ui.CurrentTool()
		slog.Debug("user's tool recorded", "tool", originalTool)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	start := time.Now()
	out, err := loop.Run(ctx, initial)
	if err != nil {
		if s != nil {
			s.markError()
		}
		return fmt.Errorf("agent: %w", err)
	}

	if s != nil && s.ui != nil && originalTool != "" {
		if err := s.ui.RestoreTool(originalTool); err != nil {
			slog.Warn("could not restore the user's tool", "tool", originalTool, "err", err)
		}
	}

	slog.Info("session complete",
		"elapsed", time.Since(start).Round(time.Millisecond),
		"turns", out.Turns, "tool_calls", out.ToolCalls, "truncated", out.Truncated)
	if out.ToolCalls == 0 {
		slog.Warn("the model wrote nothing", "final_text", out.FinalText)
	}
	if dryRun && tool.lastText != "" {
		fmt.Printf("--- model reply (%d chars) ---\n%s\n--- typeset: %d lines, %d overflow ---\n",
			len([]rune(tool.lastText)), tool.lastText, tool.lines, tool.overflow)
	}
	if out.FinalText != "" {
		slog.Info("model closing note", "text", out.FinalText)
	}
	return nil
}

// preflight verifies the model API is reachable before any page action (T2.6).
// Any HTTP response counts — this checks routing and TLS, not auth.
func preflight(cfg config.Config) error {
	base := cfg.API.BaseURL
	if base == "" {
		switch cfg.API.Provider {
		case "anthropic":
			base = "https://api.anthropic.com"
		default:
			base = "https://api.openai.com"
		}
	}
	hc := &http.Client{Timeout: 5 * time.Second}
	resp, err := hc.Get(base)
	if err != nil {
		return fmt.Errorf("API endpoint %s is unreachable (no network on the device?): %w", base, err)
	}
	resp.Body.Close()
	slog.Debug("api preflight ok", "base", base, "status", resp.StatusCode)
	return nil
}

// writeTextTool is the M2 write_text: typeset the model's text into the free
// area and inject it. In dry-run it records instead of writing.
type writeTextTool struct {
	face *layout.Face
	opt  layout.TextOptions
	s    *session // nil in offline mode
	dry  bool

	lastText string
	lines    int
	overflow int
}

func (t *writeTextTool) Def() llm.Tool {
	return llm.Tool{
		Name:        "write_text",
		Description: "Write text onto the page below the user's ink, in a handwriting-style pen font. Call it once with the complete reply. Supports Chinese and English; \\n makes a line break.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"lines": {"type": "string", "description": "the full text to write"}
			},
			"required": ["lines"]
		}`),
	}
}

func (t *writeTextTool) Run(ctx context.Context, input json.RawMessage) (agent.Result, error) {
	var in struct {
		Lines string `json:"lines"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return agent.Result{}, fmt.Errorf("write_text: bad input: %v", err)
	}
	if strings.TrimSpace(in.Lines) == "" {
		return agent.Result{}, fmt.Errorf("write_text: lines is empty")
	}

	res := layout.Text(t.face, in.Lines, t.opt)
	t.lastText = in.Lines
	t.lines += len(res.Lines)
	t.overflow += len(res.Overflow)

	if !t.dry {
		if err := t.s.writeStrokes(res.Strokes, true, true); err != nil {
			return agent.Result{}, err
		}
	} else {
		slog.Info("dry-run: not writing", "lines", len(res.Lines), "strokes", len(res.Strokes))
	}

	// Advance the writable area past what was just written, so a second call
	// (larger turn budgets) continues below instead of over it.
	used := float64(len(res.Lines)) * t.opt.CapHeightPx * t.opt.LineSpacing
	t.opt.Area.Y += used
	t.opt.Area.H -= used

	if len(res.Overflow) > 0 {
		return agent.Result{Text: fmt.Sprintf("ok: wrote %d lines, but these %d lines did NOT fit and were not written:\n%s",
			len(res.Lines), len(res.Overflow), strings.Join(res.Overflow, "\n"))}, nil
	}
	return agent.Result{Text: "ok"}, nil
}

// loadGrayPNG reads a PNG and converts it to grayscale.
func loadGrayPNG(path string) (*image.Gray, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	g := image.NewGray(img.Bounds())
	draw.Draw(g, g.Bounds(), img, img.Bounds().Min, draw.Src)
	return g, nil
}
