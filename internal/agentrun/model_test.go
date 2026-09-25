package agentrun_test

import (
	"testing"

	"github.com/novaforge/novaforge/internal/agentrun"
)

// TestNewModelClientPassesProviderThrough asserts that swapping the
// configured provider string changes only the resolved model's identity,
// never which code path builds it: NewModelClient is the sole place in
// NovaForge that names a provider, and it does so by handing the string to
// go-ai-sdk untouched.
func TestNewModelClientPassesProviderThrough(t *testing.T) {
	cases := []struct {
		provider string
		model    string
		wantID   string
	}{
		{provider: "anthropic", model: "claude-sonnet-5", wantID: "anthropic/claude-sonnet-5"},
		{provider: "openai", model: "gpt-4o", wantID: "openai/gpt-4o"},
		{provider: "", model: "local-model", wantID: "local-model"},
	}

	for _, tc := range cases {
		m, err := agentrun.NewModelClient(agentrun.ModelConfig{
			Provider: tc.provider,
			Endpoint: "http://127.0.0.1:1/v1",
			Model:    tc.model,
		})
		if err != nil {
			t.Fatalf("NewModelClient(%q, %q): %v", tc.provider, tc.model, err)
		}
		if got := m.ModelID(); got != tc.wantID {
			t.Fatalf("ModelID() = %q, want %q", got, tc.wantID)
		}
	}
}

func TestNewModelClientRequiresModel(t *testing.T) {
	if _, err := agentrun.NewModelClient(agentrun.ModelConfig{Provider: "anthropic"}); err == nil {
		t.Fatal("expected an error when Model is empty")
	}
}

func TestNewModelClientRequiresExplicitGateway(t *testing.T) {
	for _, endpoint := range []string{"", "not-a-url", "ftp://models.example/v1", "https://user:password@models.example/v1"} {
		if _, err := agentrun.NewModelClient(agentrun.ModelConfig{Model: "local", Endpoint: endpoint}); err == nil {
			t.Errorf("accepted implicit or invalid gateway %q", endpoint)
		}
	}
}
