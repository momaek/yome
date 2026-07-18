package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/momaek/yome/internal/llm"
)

// scriptedClient replays canned responses and records the requests.
type scriptedClient struct {
	responses []*llm.Response
	requests  []llm.Request
	err       error
}

func (s *scriptedClient) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	s.requests = append(s.requests, req)
	if s.err != nil {
		return nil, s.err
	}
	if len(s.responses) == 0 {
		return nil, errors.New("script exhausted")
	}
	r := s.responses[0]
	s.responses = s.responses[1:]
	return r, nil
}

// echoTool records invocations.
type echoTool struct {
	name  string
	calls []string
	fail  error
}

func (e *echoTool) Def() llm.Tool {
	return llm.Tool{Name: e.name, Description: "test", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (e *echoTool) Run(_ context.Context, input json.RawMessage) (Result, error) {
	e.calls = append(e.calls, string(input))
	if e.fail != nil {
		return Result{}, e.fail
	}
	return Result{Text: "ok"}, nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func toolUse(id, name, input string) llm.Part {
	return llm.Part{Type: llm.PartToolUse, ID: id, Name: name, Input: json.RawMessage(input)}
}

func TestLoopFinishesWithoutTools(t *testing.T) {
	c := &scriptedClient{responses: []*llm.Response{
		{Parts: []llm.Part{llm.TextPart("nothing to do")}, StopReason: "end_turn"},
	}}
	l := &Loop{Client: c, MaxTurns: 8, Log: quiet()}
	out, err := l.Run(context.Background(), []llm.Part{llm.TextPart("go")})
	if err != nil {
		t.Fatal(err)
	}
	if out.FinalText != "nothing to do" || out.ToolCalls != 0 || out.Truncated {
		t.Errorf("outcome = %+v", out)
	}
}

func TestLoopExecutesToolAndFeedsResultBack(t *testing.T) {
	tool := &echoTool{name: "write_text"}
	c := &scriptedClient{responses: []*llm.Response{
		{Parts: []llm.Part{toolUse("tu1", "write_text", `{"lines":"hi"}`)}},
		{Parts: []llm.Part{llm.TextPart("done")}},
	}}
	l := &Loop{Client: c, Tools: []Tool{tool}, MaxTurns: 8, Log: quiet()}

	out, err := l.Run(context.Background(), []llm.Part{llm.TextPart("go")})
	if err != nil {
		t.Fatal(err)
	}
	if len(tool.calls) != 1 || tool.calls[0] != `{"lines":"hi"}` {
		t.Fatalf("tool calls = %v", tool.calls)
	}
	if out.FinalText != "done" || out.ToolCalls != 1 || out.Turns != 2 {
		t.Errorf("outcome = %+v", out)
	}

	// The second request must carry the tool result for tu1.
	second := c.requests[1]
	last := second.Messages[len(second.Messages)-1]
	if last.Role != "user" || last.Parts[0].Type != llm.PartToolResult || last.Parts[0].ToolUseID != "tu1" {
		t.Errorf("tool result not fed back: %+v", last)
	}
	if last.Parts[0].Result != "ok" || last.Parts[0].IsError {
		t.Errorf("result = %+v", last.Parts[0])
	}
}

func TestLoopStopsAtMaxTurns(t *testing.T) {
	tool := &echoTool{name: "write_text"}
	c := &scriptedClient{responses: []*llm.Response{
		{Parts: []llm.Part{toolUse("tu1", "write_text", `{}`)}},
		// Would loop forever if the budget were ignored.
		{Parts: []llm.Part{toolUse("tu2", "write_text", `{}`)}},
	}}
	l := &Loop{Client: c, Tools: []Tool{tool}, MaxTurns: 1, Log: quiet()}

	out, err := l.Run(context.Background(), []llm.Part{llm.TextPart("go")})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated {
		t.Error("budget stop not reported")
	}
	// The tool still ran — the budget cuts the conversation, not the action.
	if len(tool.calls) != 1 || out.Turns != 1 {
		t.Errorf("calls=%d turns=%d", len(tool.calls), out.Turns)
	}
}

func TestLoopTurnsToolErrorsIntoResults(t *testing.T) {
	tool := &echoTool{name: "write_text", fail: fmt.Errorf("page is full")}
	c := &scriptedClient{responses: []*llm.Response{
		{Parts: []llm.Part{toolUse("tu1", "write_text", `{}`)}},
		{Parts: []llm.Part{llm.TextPart("understood")}},
	}}
	l := &Loop{Client: c, Tools: []Tool{tool}, MaxTurns: 8, Log: quiet()}

	if _, err := l.Run(context.Background(), []llm.Part{llm.TextPart("go")}); err != nil {
		t.Fatalf("a tool error must not abort the loop: %v", err)
	}
	last := c.requests[1].Messages[len(c.requests[1].Messages)-1]
	if !last.Parts[0].IsError || last.Parts[0].Result != "page is full" {
		t.Errorf("error result = %+v", last.Parts[0])
	}
}

func TestLoopHandlesUnknownTool(t *testing.T) {
	c := &scriptedClient{responses: []*llm.Response{
		{Parts: []llm.Part{toolUse("tu1", "erase_page", `{}`)}},
		{Parts: []llm.Part{llm.TextPart("oops")}},
	}}
	// erase_page is not registered — the permission-gating design depends on
	// unregistered tools failing safely (plan 4.3).
	l := &Loop{Client: c, Tools: []Tool{&echoTool{name: "write_text"}}, MaxTurns: 8, Log: quiet()}

	if _, err := l.Run(context.Background(), []llm.Part{llm.TextPart("go")}); err != nil {
		t.Fatal(err)
	}
	last := c.requests[1].Messages[len(c.requests[1].Messages)-1]
	if !last.Parts[0].IsError {
		t.Error("unknown tool did not come back as an error result")
	}
}

func TestLoopPropagatesClientErrors(t *testing.T) {
	c := &scriptedClient{err: errors.New("api down")}
	l := &Loop{Client: c, MaxTurns: 8, Log: quiet()}
	if _, err := l.Run(context.Background(), []llm.Part{llm.TextPart("go")}); err == nil {
		t.Fatal("client error swallowed")
	}
}
