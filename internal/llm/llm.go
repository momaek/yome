// Package llm is the thin, provider-neutral model client: messages with text,
// images, and tool calls in; text and tool calls out. Two wire adapters
// (Anthropic messages, OpenAI chat completions) translate this one internal
// shape, so the agent loop never sees provider details.
//
// The model MUST support vision — looking at the page is the daemon's only
// way to read what the user wrote.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Part kinds.
const (
	PartText       = "text"
	PartImage      = "image"
	PartToolUse    = "tool_use"
	PartToolResult = "tool_result"
)

// Part is one piece of message content.
type Part struct {
	Type string

	// PartText
	Text string

	// PartImage
	MediaType string // e.g. "image/png"
	Data      []byte // raw bytes; adapters handle base64

	// PartToolUse (model -> us)
	ID    string
	Name  string
	Input json.RawMessage

	// PartToolResult (us -> model)
	ToolUseID string
	Result    string
	IsError   bool
}

// TextPart builds a text part.
func TextPart(s string) Part { return Part{Type: PartText, Text: s} }

// ImagePart builds an image part from raw bytes.
func ImagePart(mediaType string, data []byte) Part {
	return Part{Type: PartImage, MediaType: mediaType, Data: data}
}

// ToolResultPart builds a tool result for a prior tool_use ID.
func ToolResultPart(id, result string, isErr bool) Part {
	return Part{Type: PartToolResult, ToolUseID: id, Result: result, IsError: isErr}
}

// Message is one conversation turn.
type Message struct {
	Role  string // "user" | "assistant"
	Parts []Part
}

// Tool is a tool definition surfaced to the model.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage // JSON Schema object
}

// Request is one model call.
type Request struct {
	System    string
	Messages  []Message
	Tools     []Tool
	MaxTokens int // 0 = DefaultMaxTokens
}

// DefaultMaxTokens bounds a response when the caller does not care.
const DefaultMaxTokens = 4096

// Response is the model's turn.
type Response struct {
	Parts      []Part // text and tool_use parts in model order
	StopReason string // provider's stop/finish reason, verbatim
}

// ToolUses filters the response's tool calls.
func (r *Response) ToolUses() []Part {
	var out []Part
	for _, p := range r.Parts {
		if p.Type == PartToolUse {
			out = append(out, p)
		}
	}
	return out
}

// Text concatenates the response's text parts.
func (r *Response) Text() string {
	var b strings.Builder
	for _, p := range r.Parts {
		if p.Type == PartText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// Config selects and tunes a provider.
type Config struct {
	Provider string // "anthropic" | "openai"
	BaseURL  string // "" = provider default; openai: self-hosted endpoints
	APIKey   string
	Model    string
	Timeout  time.Duration // per attempt
	Retries  int           // extra attempts on 429/5xx/transport errors
}

// Client makes model calls.
type Client interface {
	Complete(ctx context.Context, req Request) (*Response, error)
}

// New builds the adapter for cfg.Provider.
func New(cfg Config) (Client, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("llm: model is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	hc := &http.Client{Timeout: cfg.Timeout}
	switch cfg.Provider {
	case "anthropic":
		return &anthropicClient{cfg: cfg, hc: hc}, nil
	case "openai":
		return &openaiClient{cfg: cfg, hc: hc}, nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q (want \"anthropic\" or \"openai\")", cfg.Provider)
	}
}

// retrying wraps one HTTP attempt with the config's retry policy. attempt
// returns (retryable, error).
func retrying(ctx context.Context, retries int, attempt func() (bool, error)) error {
	var lastErr error
	for try := 0; ; try++ {
		retryable, err := attempt()
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable || try >= retries {
			return lastErr
		}
		select {
		case <-time.After(time.Duration(try+1) * 2 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
