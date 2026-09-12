package swarm_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/novaforge/novaforge/internal/agentrun"
	"github.com/novaforge/novaforge/internal/swarm"
	"github.com/novaforge/novaforge/internal/work"
)

// TestDecomposeAgainstTheConfiguredModel exercises the planner against a
// real model rather than a stub. It is skipped unless AI_ENDPOINT, AI_MODEL
// and AI_API_KEY are set, because it needs a served model — the point of
// the test is that structured output survives the round trip through a real
// gateway, which no in-process double can prove.
func TestDecomposeAgainstTheConfiguredModel(t *testing.T) {
	endpoint, model, key := os.Getenv("AI_ENDPOINT"), os.Getenv("AI_MODEL"), os.Getenv("AI_API_KEY")
	if endpoint == "" || model == "" {
		t.Skip("AI_ENDPOINT/AI_MODEL not set")
	}
	client, err := agentrun.NewModelClient(agentrun.ModelConfig{Endpoint: endpoint, Model: model, APIKey: key})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	providerOptions, err := agentrun.ParseProviderOptions(os.Getenv("AI_PROVIDER_OPTIONS"))
	if err != nil {
		t.Fatalf("ParseProviderOptions: %v", err)
	}
	p := swarm.NewPlanner(client, nil, swarm.DefaultAgentRoles)
	p.ProviderOptions = providerOptions

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	subs, err := p.Decompose(ctx, work.Item{
		Key:        "NF-1",
		Type:       "feature",
		Goal:       "Add enterprise single sign-on via OIDC",
		Acceptance: []string{"an administrator can configure an OIDC provider", "a user can sign in through it"},
	}, swarm.Bundle{})
	if err != nil {
		t.Fatalf("Decompose against %s: %v", model, err)
	}
	if len(subs) < 3 {
		t.Fatalf("want at least 3 subtasks, got %d: %+v", len(subs), subs)
	}
	for _, s := range subs {
		t.Logf("%s [%s/%s] depends on %v: %s", s.Key, s.Type, s.AgentRole, s.DependsOn, s.Title)
	}
}
