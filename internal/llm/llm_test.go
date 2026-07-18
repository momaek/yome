package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testCfg(provider, base string) Config {
	return Config{Provider: provider, BaseURL: base, APIKey: "k", Model: "m", Timeout: 5 * time.Second}
}

func request() Request {
	return Request{
		System: "sys",
		Messages: []Message{{
			Role: "user",
			Parts: []Part{
				ImagePart("image/png", []byte{1, 2, 3}),
				TextPart("hello"),
			},
		}},
		Tools: []Tool{{
			Name:        "write_text",
			Description: "write",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	}
}

func TestNewRejectsUnknownProvider(t *testing.T) {
	if _, err := New(Config{Provider: "gemini", Model: "m"}); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if _, err := New(Config{Provider: "anthropic"}); err == nil {
		t.Fatal("missing model accepted")
	}
}

func TestAnthropicWireFormat(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") == "" {
			t.Error("missing auth headers")
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"content":[
			{"type":"text","text":"I will write."},
			{"type":"tool_use","id":"tu1","name":"write_text","input":{"lines":"hi"}}
		],"stop_reason":"tool_use"}`))
	}))
	defer srv.Close()

	c, err := New(testCfg("anthropic", srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Complete(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}

	if got["system"] != "sys" || got["model"] != "m" {
		t.Errorf("system/model not carried: %v", got)
	}
	if _, ok := got["tools"].([]any); !ok {
		t.Error("tools missing")
	}
	msgs := got["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	img := content[0].(map[string]any)
	if img["type"] != "image" {
		t.Errorf("first part = %v, want image", img["type"])
	}
	src := img["source"].(map[string]any)
	if src["media_type"] != "image/png" || src["data"] != base64.StdEncoding.EncodeToString([]byte{1, 2, 3}) {
		t.Error("image source wrong")
	}

	uses := resp.ToolUses()
	if len(uses) != 1 || uses[0].Name != "write_text" || uses[0].ID != "tu1" {
		t.Fatalf("tool uses = %+v", uses)
	}
	if resp.Text() != "I will write." {
		t.Errorf("text = %q", resp.Text())
	}
}

func TestAnthropicToolResultRoundTrip(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()
	c, _ := New(testCfg("anthropic", srv.URL))

	req := Request{Messages: []Message{
		{Role: "user", Parts: []Part{TextPart("go")}},
		{Role: "assistant", Parts: []Part{{Type: PartToolUse, ID: "tu1", Name: "write_text", Input: json.RawMessage(`{}`)}}},
		{Role: "user", Parts: []Part{ToolResultPart("tu1", "ok", false)}},
	}}
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	msgs := got["messages"].([]any)
	last := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if last["type"] != "tool_result" || last["tool_use_id"] != "tu1" || last["content"] != "ok" {
		t.Errorf("tool_result wire form wrong: %v", last)
	}
}

func TestAnthropicToolResultWithImage(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()
	c, _ := New(testCfg("anthropic", srv.URL))

	req := Request{Messages: []Message{
		{Role: "user", Parts: []Part{TextPart("go")}},
		{Role: "assistant", Parts: []Part{{Type: PartToolUse, ID: "tu1", Name: "read_page", Input: json.RawMessage(`{}`)}}},
		{Role: "user", Parts: []Part{ToolResultPart("tu1", "page -1", false, ImagePart("image/png", []byte{9, 9}))}},
	}}
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	msgs := got["messages"].([]any)
	tr := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if tr["type"] != "tool_result" || tr["tool_use_id"] != "tu1" {
		t.Fatalf("tool_result wire form wrong: %v", tr)
	}
	// With an image the content must be a part array: [text, image].
	inner, ok := tr["content"].([]any)
	if !ok || len(inner) != 2 {
		t.Fatalf("content should be a 2-part array, got %v", tr["content"])
	}
	if inner[0].(map[string]any)["text"] != "page -1" {
		t.Errorf("text part wrong: %v", inner[0])
	}
	src := inner[1].(map[string]any)["source"].(map[string]any)
	if src["data"] != base64.StdEncoding.EncodeToString([]byte{9, 9}) {
		t.Errorf("image part wrong: %v", inner[1])
	}
}

func TestOpenAIWireFormat(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("authorization") != "Bearer k" {
			t.Error("missing bearer token")
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{
			"role":"assistant","content":"",
			"tool_calls":[{"id":"c1","type":"function","function":{"name":"write_text","arguments":"{\"lines\":\"hi\"}"}}]
		}}]}`))
	}))
	defer srv.Close()

	c, err := New(testCfg("openai", srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Complete(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}

	msgs := got["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Error("system message missing")
	}
	user := msgs[1].(map[string]any)
	content := user["content"].([]any)
	img := content[0].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(img, "data:image/png;base64,") {
		t.Errorf("image url = %s", img)
	}
	tools := got["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "write_text" {
		t.Error("tool definition wrong")
	}

	uses := resp.ToolUses()
	if len(uses) != 1 || uses[0].ID != "c1" || string(uses[0].Input) != `{"lines":"hi"}` {
		t.Fatalf("tool uses = %+v", uses)
	}
}

func TestOpenAIToolResultBecomesToolRole(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer srv.Close()
	c, _ := New(testCfg("openai", srv.URL))

	req := Request{Messages: []Message{
		{Role: "user", Parts: []Part{TextPart("go")}},
		{Role: "assistant", Parts: []Part{{Type: PartToolUse, ID: "c1", Name: "write_text", Input: json.RawMessage(`{}`)}}},
		{Role: "user", Parts: []Part{ToolResultPart("c1", "ok", false)}},
	}}
	resp, err := c.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text() != "done" {
		t.Errorf("text = %q", resp.Text())
	}
	msgs := got["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "tool" || last["tool_call_id"] != "c1" {
		t.Errorf("tool result should be a tool-role message: %v", last)
	}
}

func TestOpenAIToolResultImageBecomesUserMessage(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer srv.Close()
	c, _ := New(testCfg("openai", srv.URL))

	req := Request{Messages: []Message{
		{Role: "user", Parts: []Part{TextPart("go")}},
		{Role: "assistant", Parts: []Part{{Type: PartToolUse, ID: "c1", Name: "read_page", Input: json.RawMessage(`{}`)}}},
		{Role: "user", Parts: []Part{ToolResultPart("c1", "page -1", false, ImagePart("image/png", []byte{9, 9}))}},
	}}
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	msgs := got["messages"].([]any)
	// ...assistant tool_calls, tool result, then the image as a user message.
	toolMsg := msgs[len(msgs)-2].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "c1" {
		t.Errorf("tool message wrong: %v", toolMsg)
	}
	imgMsg := msgs[len(msgs)-1].(map[string]any)
	if imgMsg["role"] != "user" {
		t.Fatalf("image should follow as a user message, got %v", imgMsg)
	}
	content := imgMsg["content"].([]any)
	url := content[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Errorf("image url = %s", url)
	}
}

func TestRetriesOn500ThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	cfg := testCfg("anthropic", srv.URL)
	cfg.Retries = 1
	c, _ := New(cfg)
	resp, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Parts: []Part{TextPart("x")}}}})
	if err != nil {
		t.Fatalf("retry did not recover: %v", err)
	}
	if resp.Text() != "ok" || calls.Load() != 2 {
		t.Errorf("text=%q calls=%d", resp.Text(), calls.Load())
	}
}

func TestNoRetryOn400(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	cfg := testCfg("anthropic", srv.URL)
	cfg.Retries = 3
	c, _ := New(cfg)
	_, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Parts: []Part{TextPart("x")}}}})
	if err == nil {
		t.Fatal("400 reported success")
	}
	if calls.Load() != 1 {
		t.Errorf("a client error was retried %d times", calls.Load()-1)
	}
}
