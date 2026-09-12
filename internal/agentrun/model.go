// Package agentrun drives one agent run's model/tool loop over go-ai-sdk:
// it assembles a request, calls the configured model, dispatches any
// requested tool through a tools.Registry, and repeats until the model
// stops requesting tools or the run's budget is exhausted. No package in
// NovaForge outside this file names an AI provider.
package agentrun

import (
	"encoding/json"
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

// ProviderOptionsKey is the provider name go-ai-sdk merges per-provider
// escape-hatch parameters under. NovaForge always builds its model client
// through the gateway provider (NewModelClient above), so every
// provider-specific wire parameter a deployment configures is keyed here.
const ProviderOptionsKey = "gateway"

// ParseProviderOptions turns a JSON object of extra wire parameters into
// the shape go-ai-sdk's ai.GenerateTextOpts.ProviderOptions expects. It
// exists because some model servers need a parameter no vendor-neutral API
// has: a Qwen server behind vLLM, for instance, keeps its chain-of-thought
// on unless the request carries {"chat_template_kwargs":
// {"enable_thinking": false}}, and a reasoning model asked for structured
// output will spend its whole token budget thinking and return nothing
// decodable. Naming such a parameter in configuration keeps it out of the
// code: NovaForge still has no per-provider branch.
//
// An empty raw yields nil options, which is the "send nothing extra" case.
func ParseProviderOptions(raw string) (map[string]any, error) {
	if raw == "" {
		return nil, nil
	}
	var opts map[string]any
	if err := json.Unmarshal([]byte(raw), &opts); err != nil {
		return nil, fmt.Errorf("agentrun: parse provider options %q: %w", raw, err)
	}
	if len(opts) == 0 {
		return nil, nil
	}
	return map[string]any{ProviderOptionsKey: opts}, nil
}
