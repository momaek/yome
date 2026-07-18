// Package agent runs the perceive-decide-act loop: screenshot in, tool calls
// out, until the model finishes or the turn budget runs dry.
//
// The loop is deliberately plain (plan 4.3): call the model; execute any tool
// calls synchronously; feed results back; repeat. Action tools are expensive
// and irreversible, so the budget is a runaway guard, not a target — a normal
// task ends by the model stopping on its own.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/momaek/yome/internal/llm"
)

// Tool is one callable surfaced to the model. Run returns the tool_result
// content; an error becomes an is_error tool result rather than aborting the
// loop, so the model gets a chance to react.
type Tool interface {
	Def() llm.Tool
	Run(ctx context.Context, input json.RawMessage) (string, error)
}

// Loop drives one session.
type Loop struct {
	Client llm.Client
	Tools  []Tool
	System string
	// MaxTurns is the maximum number of tool-executing rounds. After the
	// final round's tools run, the loop stops without another model call.
	MaxTurns int
	Log      *slog.Logger
}

// Outcome summarises a finished session.
type Outcome struct {
	FinalText string // the model's closing text, if it produced one
	ToolCalls int    // tools actually executed
	Turns     int    // model calls made
	Truncated bool   // stopped by MaxTurns rather than the model finishing
}

// Run starts the conversation with the given user parts (screenshot +
// instruction) and loops until the model stops calling tools.
func (l *Loop) Run(ctx context.Context, initial []llm.Part) (*Outcome, error) {
	log := l.Log
	if log == nil {
		log = slog.Default()
	}
	maxTurns := l.MaxTurns
	if maxTurns < 1 {
		maxTurns = 1
	}

	byName := make(map[string]Tool, len(l.Tools))
	defs := make([]llm.Tool, 0, len(l.Tools))
	for _, t := range l.Tools {
		d := t.Def()
		byName[d.Name] = t
		defs = append(defs, d)
	}

	messages := []llm.Message{{Role: "user", Parts: initial}}
	out := &Outcome{}

	for round := 0; ; round++ {
		resp, err := l.Client.Complete(ctx, llm.Request{
			System:   l.System,
			Messages: messages,
			Tools:    defs,
		})
		if err != nil {
			return out, err
		}
		out.Turns++
		messages = append(messages, llm.Message{Role: "assistant", Parts: resp.Parts})

		uses := resp.ToolUses()
		if len(uses) == 0 {
			out.FinalText = resp.Text()
			log.Debug("agent finished", "turns", out.Turns, "tool_calls", out.ToolCalls)
			return out, nil
		}

		var results []llm.Part
		for _, u := range uses {
			result, isErr := l.dispatch(ctx, log, byName, u)
			out.ToolCalls++
			results = append(results, llm.ToolResultPart(u.ID, result, isErr))
		}

		if round+1 >= maxTurns {
			// Budget spent: the actions ran, but the model gets no closing
			// word. Deliberate for M2's single-shot form; the guard case for
			// larger budgets logs loudly because it usually means looping.
			out.Truncated = true
			out.FinalText = resp.Text()
			log.Info("agent stopped at the turn budget", "max_turns", maxTurns, "tool_calls", out.ToolCalls)
			return out, nil
		}
		messages = append(messages, llm.Message{Role: "user", Parts: results})
	}
}

// dispatch runs one tool call, mapping every failure into a tool_result the
// model can see.
func (l *Loop) dispatch(ctx context.Context, log *slog.Logger, byName map[string]Tool, u llm.Part) (string, bool) {
	t, ok := byName[u.Name]
	if !ok {
		log.Warn("model called an unregistered tool", "tool", u.Name)
		return fmt.Sprintf("unknown tool %q", u.Name), true
	}
	log.Info("tool call", "tool", u.Name, "input_bytes", len(u.Input))
	result, err := t.Run(ctx, u.Input)
	if err != nil {
		log.Warn("tool failed", "tool", u.Name, "err", err)
		return err.Error(), true
	}
	return result, false
}
