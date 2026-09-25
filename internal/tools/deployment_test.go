package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/deployment"
	"github.com/novaforge/novaforge/internal/tools"
)

type deploymentRPCFixture struct {
	requested deployment.Request
	operation deployment.Operation
	executed  int
}

func (c *deploymentRPCFixture) Request(_ context.Context, r deployment.Request) (deployment.Operation, error) {
	c.requested = r
	c.operation = deployment.Operation{Request: r}
	return c.operation, nil
}
func (c *deploymentRPCFixture) Get(context.Context, uuid.UUID) (deployment.Operation, error) {
	return c.operation, nil
}
func (c *deploymentRPCFixture) Execute(context.Context, uuid.UUID) (deployment.Operation, error) {
	c.executed++
	return c.operation, nil
}
func (c *deploymentRPCFixture) Retry(ctx context.Context, id uuid.UUID) (deployment.Operation, error) {
	return c.Execute(ctx, id)
}
func (c *deploymentRPCFixture) Reconcile(context.Context, uuid.UUID) (deployment.Operation, error) {
	return c.operation, nil
}

func TestDeploymentToolsBindRunAndRejectTargetEscalation(t *testing.T) {
	pool := auditPool(t)
	org, repo := uuid.New(), uuid.New()
	ctx := scopedCtx(org)
	run := newTestRun(t, ctx, agents.NewStore(pool), org)
	registry := tools.NewRegistry(tools.Runtime{RunID: run.ID}, agents.NewAuditLog(pool))
	client := &deploymentRPCFixture{}
	tools.RegisterDeploymentTools(registry, client, repo)
	id := uuid.New()
	raw, _ := json.Marshal(map[string]string{"id": id.String(), "target": "approved-target", "artifact": "sha256:artifact-fixture"})
	if _, err := registry.Call(ctx, run.ID, "deployment.request", raw); err != nil {
		t.Fatal(err)
	}
	if client.requested.RepoID != repo || client.requested.RunID != run.ID {
		t.Fatal("model arguments chose scope")
	}
	escalations := []string{`{"id":"` + id.String() + `","target":"approved-target","artifact":"x","repo_id":"` + uuid.NewString() + `"}`, `{"id":"` + id.String() + `","namespace":"production"}`, `null`, `{} {}`}
	for _, raw := range escalations {
		if _, err := registry.Call(ctx, run.ID, "deployment.request", []byte(raw)); err == nil {
			t.Fatalf("unexpected target field accepted: %s", raw)
		}
	}
	args := []byte(`{"id":"` + id.String() + `"}`)
	if _, err := registry.Call(ctx, run.ID, "deployment.execute", args); err != nil {
		t.Fatal(err)
	}
	client.operation.RunID = uuid.New()
	if _, err := registry.Call(ctx, run.ID, "deployment.execute", args); err == nil {
		t.Fatal("another run's operation executed")
	}
	if client.executed != 1 {
		t.Fatal("foreign execution reached service")
	}
	registry.Restrict([]string{"deployment.status"})
	if _, err := registry.Call(ctx, run.ID, "deployment.execute", args); err == nil {
		t.Fatal("removed deployment tool callable")
	}
}

func TestDeploymentToolsMissingServiceIsUnavailable(t *testing.T) {
	pool := auditPool(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run := newTestRun(t, ctx, agents.NewStore(pool), org)
	registry := tools.NewRegistry(tools.Runtime{RunID: run.ID}, agents.NewAuditLog(pool))
	tools.RegisterDeploymentTools(registry, nil, uuid.New())
	if _, err := registry.Call(ctx, run.ID, "deployment.status", []byte(`{"id":"`+uuid.NewString()+`"}`)); err == nil {
		t.Fatal("unavailable deployment service reported success")
	}
}
