package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const anthropicDefaultBase = "https://api.anthropic.com"

// anthropicClient speaks the Anthropic Messages API.
type anthropicClient struct {
	cfg Config
	hc  *http.Client
}

type anthReq struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	System    string        `json:"system,omitempty"`
	Messages  []anthMessage `json:"messages"`
	Tools     []anthTool    `json:"tools,omitempty"`
}

type anthMessage struct {
	Role    string     `json:"role"`
	Content []anthPart `json:"content"`
}

type anthPart struct {
	Type string `json:"type"`

	Text string `json:"text,omitempty"`

	Source *anthImageSource `json:"source,omitempty"`

	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type anthImageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthResp struct {
	Content    []anthPart `json:"content"`
	StopReason string     `json:"stop_reason"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *anthropicClient) Complete(ctx context.Context, req Request) (*Response, error) {
	body := anthReq{
		Model:     c.cfg.Model,
		MaxTokens: req.MaxTokens,
		System:    req.System,
	}
	if body.MaxTokens == 0 {
		body.MaxTokens = DefaultMaxTokens
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, anthTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	for _, m := range req.Messages {
		am := anthMessage{Role: m.Role}
		for _, p := range m.Parts {
			switch p.Type {
			case PartText:
				am.Content = append(am.Content, anthPart{Type: "text", Text: p.Text})
			case PartImage:
				am.Content = append(am.Content, anthPart{Type: "image", Source: &anthImageSource{
					Type: "base64", MediaType: p.MediaType, Data: base64.StdEncoding.EncodeToString(p.Data),
				}})
			case PartToolUse:
				am.Content = append(am.Content, anthPart{Type: "tool_use", ID: p.ID, Name: p.Name, Input: p.Input})
			case PartToolResult:
				am.Content = append(am.Content, anthPart{Type: "tool_result", ToolUseID: p.ToolUseID, Content: p.Result, IsError: p.IsError})
			default:
				return nil, fmt.Errorf("anthropic: unknown part type %q", p.Type)
			}
		}
		body.Messages = append(body.Messages, am)
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}

	base := c.cfg.BaseURL
	if base == "" {
		base = anthropicDefaultBase
	}
	url := strings.TrimSuffix(base, "/") + "/v1/messages"

	var out *Response
	err = retrying(ctx, c.cfg.Retries, func() (bool, error) {
		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return false, err
		}
		hreq.Header.Set("content-type", "application/json")
		hreq.Header.Set("x-api-key", c.cfg.APIKey)
		hreq.Header.Set("anthropic-version", "2023-06-01")

		resp, err := c.hc.Do(hreq)
		if err != nil {
			return true, fmt.Errorf("anthropic: %w", err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return true, fmt.Errorf("anthropic: read response: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			retryable := resp.StatusCode == 429 || resp.StatusCode >= 500
			return retryable, fmt.Errorf("anthropic: HTTP %d: %s", resp.StatusCode, errSnippet(raw))
		}

		var ar anthResp
		if err := json.Unmarshal(raw, &ar); err != nil {
			return false, fmt.Errorf("anthropic: decode response: %w", err)
		}
		r := &Response{StopReason: ar.StopReason}
		for _, p := range ar.Content {
			switch p.Type {
			case "text":
				r.Parts = append(r.Parts, TextPart(p.Text))
			case "tool_use":
				r.Parts = append(r.Parts, Part{Type: PartToolUse, ID: p.ID, Name: p.Name, Input: p.Input})
			}
		}
		out = r
		return false, nil
	})
	return out, err
}

// errSnippet keeps provider error bodies loggable without dumping megabytes.
func errSnippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 500 {
		s = s[:500] + "..."
	}
	return s
}
