package agents

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TokenPrice is what one model's tokens cost, in micro-units of the
// deployment's currency per million tokens.
//
// A run's cost limit is only meaningful against a price, and NovaForge
// invents none: a model served on the deployment's own hardware has no list
// price, and a plausible default would make a cost limit look enforced while
// it measured nothing. An operator states the price; without one, a cost
// limit is refused rather than accepted and silently never reached.
type TokenPrice struct {
	InputMicrosPerMillion  int64 `json:"input_micros_per_million_tokens"`
	OutputMicrosPerMillion int64 `json:"output_micros_per_million_tokens"`
}

// CostMicros is the cost of one model response's usage. It rounds up: a
// response is never free because it was small, which would let a run made
// of many small calls creep past its limit.
func (p TokenPrice) CostMicros(inputTokens, outputTokens int) int64 {
	total := int64(inputTokens)*p.InputMicrosPerMillion + int64(outputTokens)*p.OutputMicrosPerMillion
	if total <= 0 {
		return 0
	}
	return (total + 999_999) / 1_000_000
}

// ParseModelPrices reads AI_MODEL_PRICES: a JSON object from model name to
// its TokenPrice. An empty value means no model is priced. A malformed value
// is an error, not "no prices" — a typo must not quietly disable every cost
// limit in the deployment.
func ParseModelPrices(raw string) (map[string]TokenPrice, error) {
	out := map[string]TokenPrice{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("AI_MODEL_PRICES is not a JSON object of model name to {input_micros_per_million_tokens, output_micros_per_million_tokens}: %w", err)
	}
	for model, p := range out {
		if p.InputMicrosPerMillion < 0 || p.OutputMicrosPerMillion < 0 {
			return nil, fmt.Errorf("AI_MODEL_PRICES: model %q has a negative price", model)
		}
		if p.InputMicrosPerMillion == 0 && p.OutputMicrosPerMillion == 0 {
			return nil, fmt.Errorf("AI_MODEL_PRICES: model %q is priced at zero, which no cost limit could ever reach; leave it out instead", model)
		}
	}
	return out, nil
}
