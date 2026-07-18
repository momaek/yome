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

const openaiDefaultBase = "https://api.openai.com/v1"

// openaiClient speaks the OpenAI chat-completions dialect, which is also what
// self-hosted stacks (vLLM, Ollama, LM Studio) expose. Vision-and-tools
// support varies by model there; that is the operator's choice to make.
type openaiClient struct {
	cfg Config
	hc  *http.Client
}

type oaReq struct {
	Model    string      `json:"model"`
	Messages []oaMessage `json:"messages"`
	Tools    []oaTool    `json:"tools,omitempty"`
	MaxTok   int         `json:"max_completion_tokens,omitempty"`
}

type oaMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"` // string or content-part array
	ToolCalls  []oaToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type oaContentPart struct {
	Type     string      `json:"type"` // "text" | "image_url"
	Text     string      `json:"text,omitempty"`
	ImageURL *oaImageURL `json:"image_url,omitempty"`
}

type oaImageURL struct {
	URL string `json:"url"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"` // "function"
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oaResp struct {
	Choices []struct {
		FinishReason string    `json:"finish_reason"`
		Message      oaMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func (c *openaiClient) Complete(ctx context.Context, req Request) (*Response, error) {
	body := oaReq{Model: c.cfg.Model, MaxTok: req.MaxTokens}
	if body.MaxTok == 0 {
		body.MaxTok = DefaultMaxTokens
	}
	if req.System != "" {
		body.Messages = append(body.Messages, oaMessage{Role: "system", Content: jsonString(req.System)})
	}
	for _, t := range req.Tools {
		var ot oaTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.InputSchema
		body.Tools = append(body.Tools, ot)
	}

	for _, m := range req.Messages {
		var parts []oaContentPart
		var toolCalls []oaToolCall
		var toolResults []oaMessage
		for _, p := range m.Parts {
			switch p.Type {
			case PartText:
				parts = append(parts, oaContentPart{Type: "text", Text: p.Text})
			case PartImage:
				parts = append(parts, oaContentPart{Type: "image_url", ImageURL: &oaImageURL{
					URL: "data:" + p.MediaType + ";base64," + base64.StdEncoding.EncodeToString(p.Data),
				}})
			case PartToolUse:
				tc := oaToolCall{ID: p.ID, Type: "function"}
				tc.Function.Name = p.Name
				tc.Function.Arguments = string(p.Input)
				toolCalls = append(toolCalls, tc)
			case PartToolResult:
				// OpenAI carries tool results as their own "tool" role messages.
				toolResults = append(toolResults, oaMessage{
					Role: "tool", ToolCallID: p.ToolUseID, Content: jsonString(p.Result),
				})
			default:
				return nil, fmt.Errorf("openai: unknown part type %q", p.Type)
			}
		}
		if len(parts) > 0 || len(toolCalls) > 0 {
			om := oaMessage{Role: m.Role, ToolCalls: toolCalls}
			if len(parts) > 0 {
				enc, err := json.Marshal(parts)
				if err != nil {
					return nil, fmt.Errorf("openai: encode content: %w", err)
				}
				om.Content = enc
			}
			body.Messages = append(body.Messages, om)
		}
		body.Messages = append(body.Messages, toolResults...)
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}

	base := c.cfg.BaseURL
	if base == "" {
		base = openaiDefaultBase
	}
	url := strings.TrimSuffix(base, "/") + "/chat/completions"

	var out *Response
	err = retrying(ctx, c.cfg.Retries, func() (bool, error) {
		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return false, err
		}
		hreq.Header.Set("content-type", "application/json")
		if c.cfg.APIKey != "" {
			hreq.Header.Set("authorization", "Bearer "+c.cfg.APIKey)
		}

		resp, err := c.hc.Do(hreq)
		if err != nil {
			return true, fmt.Errorf("openai: %w", err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return true, fmt.Errorf("openai: read response: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			retryable := resp.StatusCode == 429 || resp.StatusCode >= 500
			return retryable, fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, errSnippet(raw))
		}

		var or oaResp
		if err := json.Unmarshal(raw, &or); err != nil {
			return false, fmt.Errorf("openai: decode response: %w", err)
		}
		if len(or.Choices) == 0 {
			return false, fmt.Errorf("openai: response has no choices: %s", errSnippet(raw))
		}
		ch := or.Choices[0]
		r := &Response{StopReason: ch.FinishReason}
		if len(ch.Message.Content) > 0 {
			// Content comes back as a plain string for text-only replies.
			var text string
			if err := json.Unmarshal(ch.Message.Content, &text); err == nil {
				if text != "" {
					r.Parts = append(r.Parts, TextPart(text))
				}
			}
		}
		for _, tc := range ch.Message.ToolCalls {
			r.Parts = append(r.Parts, Part{
				Type: PartToolUse, ID: tc.ID, Name: tc.Function.Name,
				Input: json.RawMessage(tc.Function.Arguments),
			})
		}
		out = r
		return false, nil
	})
	return out, err
}
