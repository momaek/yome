// The ai command: one full perceive-decide-act session. M2 ran it as the
// maxTurns=1 write_text degenerate form; M3 gives the model the full toolset
// (read_page/new_page/draw, and erase_page in in-place mode) and the v2
// prompt. serve triggers exactly this flow from a gesture.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/momaek/yome/internal/agent"
	"github.com/momaek/yome/internal/capture"
	"github.com/momaek/yome/internal/config"
	"github.com/momaek/yome/internal/geom"
	"github.com/momaek/yome/internal/layout"
	"github.com/momaek/yome/internal/llm"
	"github.com/momaek/yome/internal/trigger"
)

func runAI(args []string) error {
	fs := flag.NewFlagSet("ai", flag.ExitOnError)
	cf := addCommon(fs)
	imagePath := fs.String("image", "", "offline replay: use this PNG instead of the device (implies -dry-run)")
	instruction := fs.String("instruction", "", "extra instruction to send alongside the screenshot")
	dry := fs.Bool("dry-run", false, "call the model but print the reply instead of writing it")
	maxTurns := fs.Int("max-turns", 0, "tool-execution rounds (default: config api.max_turns)")
	mode := fs.String("mode", "new-page", "session mode: new-page or in-place (in-place registers erase_page)")
	if err := parse(fs, cf, args); err != nil {
		return err
	}

	var sessionMode trigger.Mode
	switch *mode {
	case "new-page":
		sessionMode = trigger.NewPage
	case "in-place":
		sessionMode = trigger.InPlace
	default:
		return fmt.Errorf("%w: -mode must be new-page or in-place, got %q", errUsage, *mode)
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

	turns := *maxTurns
	if turns <= 0 {
		turns = cfg.API.MaxTurns
	}
	out, tb, err := runAgentSession(context.Background(), cfg, client, s, viewImg, area, sessionMode, dryRun, turns, *instruction)
	if err != nil {
		return err
	}

	if dryRun && tb.lastText != "" {
		fmt.Printf("--- model reply (%d chars) ---\n%s\n--- typeset: %d lines, %d overflow ---\n",
			len([]rune(tb.lastText)), tb.lastText, tb.lines, tb.overflow)
	}
	if out.FinalText != "" {
		slog.Info("model closing note", "text", out.FinalText)
	}
	return nil
}

// runAgentSession is the shared heart of ai and serve: prompt assembly, tool
// assembly, session etiquette, and the loop itself. s is nil offline.
func runAgentSession(ctx context.Context, cfg config.Config, client llm.Client, s *session, viewImg *image.Gray, area geom.Rect, mode trigger.Mode, dryRun bool, maxTurns int, instruction string) (*agent.Outcome, *toolbox, error) {
	face := layout.DefaultFace()
	capH := cfg.Layout.CapHeightPx
	free := freeAreaBelowInk(viewImg, area, capH)
	opt := layout.TextOptions{CapHeightPx: capH, LineSpacing: cfg.Layout.LineSpacing, Area: free}
	fullOpt := opt
	fullOpt.Area = area

	// A crowded page no longer blocks the session (M2 refused here): the
	// model can new_page its way to room, and in-place mode erases anyway.
	if lines := free.H / (capH * layout.MinLineSpacing); lines < 2 {
		slog.Info("almost no free space below the ink; the model will need new_page", "free_h", free.H)
	}

	system := agent.SystemPrompt(agent.PromptParams{
		PageW: viewImg.Bounds().Dx(), PageH: viewImg.Bounds().Dy(),
		FreeW: int(free.W), FreeH: int(free.H),
		LatinBudget:     layout.PageBudget(face, opt),
		HanBudget:       layout.HanPageBudget(face, opt),
		PageLatinBudget: layout.PageBudget(face, fullOpt),
		PageHanBudget:   layout.HanPageBudget(face, fullOpt),
		InPlace:         mode == trigger.InPlace,
		MaxTurns:        maxTurns,
	})
	slog.Debug("free area", "rect", free, "latin_budget", layout.PageBudget(face, opt))

	var png bytes.Buffer
	if err := capture.WritePNG(&png, viewImg); err != nil {
		return nil, nil, err
	}

	tb := &toolbox{s: s, face: face, opt: opt, dry: dryRun}
	initial := []llm.Part{llm.ImagePart("image/png", png.Bytes())}
	msg := instruction
	if msg == "" {
		msg = "The user triggered you on this page. Follow your instructions."
		if mode == trigger.InPlace {
			msg = "The user triggered you on this page with the IN-PLACE gesture: they want this page erased and rewritten better. Follow your instructions."
		}
	}
	initial = append(initial, llm.TextPart(msg))

	loop := &agent.Loop{
		Client: client, Tools: assembleTools(tb, mode),
		System: system, MaxTurns: maxTurns,
	}

	// Session etiquette (T2.5): remember the user's tool and pen style, put
	// both back after. The style must be read before the loop runs — the
	// model's own set_pen taps make xochitl overwrite LastWritingTool.
	var originalTool string
	if s != nil && s.ui != nil {
		s.orientation()
		originalTool = s.ui.CurrentTool()
		slog.Debug("user's tool recorded", "tool", originalTool)
		s.recordPenState()
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	start := time.Now()
	out, err := loop.Run(ctx, initial)
	if err != nil && s != nil && !errors.Is(err, context.Canceled) {
		// Feedback first: the corner mark must not queue behind the restore
		// taps below (same lesson as markBusy). A user cancel is not an
		// error — no × for doing what was asked.
		s.markError()
	}

	if s != nil && s.ui != nil {
		// Restore when the model styled the pen, and also when the daemon
		// forced its configured writing pen (ensurePen): both leave the
		// user's own pen replaced, so both owe a restore.
		if tb.styled || s.penStyled {
			// Style before tool: the panel path leaves the pen selected, so
			// a non-pen original tool must be re-selected afterwards.
			s.restorePenStyle()
		}
		if originalTool != "" {
			if err := s.ui.RestoreTool(originalTool); err != nil {
				slog.Warn("could not restore the user's tool", "tool", originalTool, "err", err)
			}
		}
	}
	if err != nil {
		return nil, tb, fmt.Errorf("agent: %w", err)
	}

	slog.Info("session complete",
		"elapsed", time.Since(start).Round(time.Millisecond),
		"turns", out.Turns, "tool_calls", out.ToolCalls, "truncated", out.Truncated)
	if out.ToolCalls == 0 {
		slog.Warn("the model wrote nothing", "final_text", out.FinalText)
	}
	return out, tb, nil
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
