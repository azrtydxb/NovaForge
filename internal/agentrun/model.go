// Package agentrun drives one agent run's model/tool loop over go-ai-sdk:
// it assembles a request, calls the configured model, dispatches any
// requested tool through a tools.Registry, and repeats until the model
// stops requesting tools or the run's budget is exhausted. No package in
// NovaForge outside this file names an AI provider.
package agentrun

import (
	"fmt"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/go-ai-sdk/providers/gateway"
)

// ModelConfig names the model an agent run talks to. Provider and Model are
// combined into go-ai-sdk's "provider/model" routing slug (e.g.
// "anthropic/claude-sonnet-5") and sent to Endpoint verbatim — neither
// value is inspected or branched on here or anywhere else in NovaForge.
// Endpoint lets an air-gapped deployment point at a self-hosted
// OpenAI-compatible router (LiteLLM, FastLLM, vLLM, ...) instead of a
// hosted gateway, with no code change: air-gapped operation never hard-
// depends on reaching a hosted provider.
type ModelConfig struct {
	Provider string
	Endpoint string
	Model    string
	APIKey   string
}

// NewModelClient resolves cfg into a provider.LanguageModel through
// go-ai-sdk's gateway provider. This is the only place in NovaForge that
// constructs a model client: cfg.Provider and cfg.Model are concatenated
// into a routing slug and handed to go-ai-sdk exactly as configured, with
// no per-provider branch — swapping providers is a configuration change,
// never a code change.
func NewModelClient(cfg ModelConfig) (provider.LanguageModel, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("agentrun: model config requires a Model")
	}

	var opts []gateway.Option
	if cfg.Endpoint != "" {
		opts = append(opts, gateway.WithBaseURL(cfg.Endpoint))
	}
	if cfg.APIKey != "" {
		opts = append(opts, gateway.WithAPIKey(cfg.APIKey))
	}

	id := cfg.Model
	if cfg.Provider != "" {
		id = cfg.Provider + "/" + cfg.Model
	}

	p := gateway.New(opts...)
	return p.Model(id), nil
}
