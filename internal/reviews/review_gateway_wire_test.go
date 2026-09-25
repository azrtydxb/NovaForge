package reviews

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/go-ai-sdk/providers/gateway"
)

// Qualify the actual review caller, not a manually constructed approximation:
// the gateway SDK forces a synthetic function tool for OutputObject because it
// deliberately does not promise NativeJSON across heterogeneous model servers.
func TestReviewGatewayStructuredOutputWire(t *testing.T) {
	var calls atomic.Int32
	bodies := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		select {
		case bodies <- body:
		default:
		}
		if req.Method != http.MethodPost || req.URL.Path != "/executions/v1/chat/completions" {
			http.Error(w, "wrong SDK route", 404)
			return
		}
		var wire struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &wire); err != nil || len(wire.Tools) != 1 {
			http.Error(w, "expected one structured-output tool", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "tool_calls",
				"message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
					"id": "synthetic-review-output", "type": "function", "function": map[string]any{
						"name": wire.Tools[0].Function.Name, "arguments": `{"verdict":"approve","summary":"controlled SDK wire fixture"}`,
					},
				}}},
			}},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	defer server.Close()
	reviewer := &AgentReviewer{
		Models:          []provider.LanguageModel{gateway.New(gateway.WithAPIKey("synthetic-fixture-only"), gateway.WithBaseURL(server.URL+"/executions/v1")).Model("review-fixture")},
		ProviderOptions: map[string]any{"gateway": map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}}},
	}
	out, usage, completed, err := reviewer.reviewBounded(context.Background(), Run{Title: "SDK contract fixture", SourceRef: "feature", TargetRef: "main", AgentName: "author"}, "security", "source-sha", "target-sha", "+ controlled source fixture", 4096)
	if err != nil || !completed || out.Verdict != "approve" || usage.TotalTokens != 15 || calls.Load() != 1 {
		t.Fatalf("actual SDK structured-output review: out=%+v usage=%+v completed=%v calls=%d error=%v", out, usage, completed, calls.Load(), err)
	}
	body := <-bodies
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"model", "messages", "max_completion_tokens", "tools", "tool_choice", "chat_template_kwargs"} {
		if _, ok := wire[key]; !ok {
			t.Fatalf("missing SDK wire field %q: %s", key, body)
		}
	}
	if len(wire) != 6 {
		t.Fatalf("unqualified SDK request fields: %s", body)
	}
	var tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(wire["tools"], &tools); err != nil || len(tools) != 1 || tools[0].Type != "function" || tools[0].Function.Name != "output" {
		t.Fatalf("expected synthetic output function: %s (%v)", wire["tools"], err)
	}
	var choice struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(wire["tool_choice"], &choice); err != nil || choice.Type != "function" || choice.Function.Name != tools[0].Function.Name {
		t.Fatalf("expected forced matching function choice: %s (%v)", wire["tool_choice"], err)
	}
	var messages []struct {
		Role    string  `json:"role"`
		Content *string `json:"content"`
	}
	if err := json.Unmarshal(wire["messages"], &messages); err != nil || len(messages) != 2 {
		t.Fatalf("expected string-valued system/user messages: %s (%v)", wire["messages"], err)
	}
	for i, role := range []string{"system", "user"} {
		if messages[i].Role != role || messages[i].Content == nil {
			t.Fatalf("expected explicit string content and ordered system/user roles: %s", wire["messages"])
		}
	}
	// All content is synthetic; this log contains no user prompts or credentials.
	t.Logf("SDK_WIRE_FIXTURE %s", body)
}
