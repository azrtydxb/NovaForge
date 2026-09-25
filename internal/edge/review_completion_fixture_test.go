package edge_test

import (
	"encoding/json"
	"testing"
	"time"
)

func queueReviewCompletion(fail bool) map[string]any {
	arguments := `{"verdict":"approve","summary":"inspected pinned change"}`
	if fail {
		// The owner accepts JSON objects, while the consumer rejects an invalid
		// application verdict. Malformed JSON would remain owner-unknown.
		arguments = `{"verdict":"invalid","summary":"inspected pinned change"}`
	}
	return map[string]any{"created": time.Now().Unix(), "id": "queue-completion", "object": "chat.completion", "model": "review-model", "choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "output", "type": "function", "function": map[string]any{"name": "output", "arguments": arguments}}}}}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}}
}

func TestQueueCompletionSatisfiesOwnerProtocol(t *testing.T) {
	for _, fail := range []bool{false, true} {
		body := queueReviewCompletion(fail)
		if _, ok := body["created"].(int64); !ok {
			t.Error("owner requires integer completion creation timestamp")
		}
		choice := body["choices"].([]any)[0].(map[string]any)
		message := choice["message"].(map[string]any)
		tool := message["tool_calls"].([]any)[0].(map[string]any)
		args := tool["function"].(map[string]any)["arguments"].(string)
		var object map[string]any
		if err := json.Unmarshal([]byte(args), &object); err != nil || object == nil {
			t.Errorf("owner requires JSON-object function arguments: %q", args)
		}
	}
}
