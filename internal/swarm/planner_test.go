package swarm_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/azrtydxb/go-ai-sdk/provider"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/swarm"
	"github.com/novaforge/novaforge/internal/work"
)

func dbURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return u
}

func storePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := dbURL(t)
	if err := database.Migrate(url, "work", os.DirFS("../work/migrations")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newWorkStore(t *testing.T) *work.Store {
	t.Helper()
	return work.NewStore(storePool(t))
}

func scopedCtx(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID:     orgID,
		ActorID:   uuid.New(),
		ActorKind: "user",
	})
}

// stubPlannerModel is an in-process test double for provider.LanguageModel:
// it always returns the same canned JSON body as its response text,
// regardless of what it was asked, and reports native JSON support so
// go-ai-sdk's structured-output path decodes that text directly rather than
// falling back to a forced tool call.
type stubPlannerModel struct {
	body string
}

func (m *stubPlannerModel) Generate(ctx context.Context, call provider.Call) (*provider.Response, error) {
	return &provider.Response{
		Content:      []provider.ContentPart{provider.TextPart{Text: m.body}},
		FinishReason: provider.FinishStop,
	}, nil
}

func (m *stubPlannerModel) Stream(ctx context.Context, call provider.Call) (provider.StreamResponse, error) {
	return nil, fmt.Errorf("stub planner model does not support streaming")
}

func (m *stubPlannerModel) ModelID() string      { return "stub-planner-model" }
func (m *stubPlannerModel) ProviderName() string { return "stub" }
func (m *stubPlannerModel) Capabilities() provider.Capabilities {
	return provider.Capabilities{NativeJSON: true}
}

// ssoDecomposition is the six-subtask Enterprise SSO example from the plan:
// database changes, OAuth backend, admin configuration, frontend,
// documentation, and integration tests.
const ssoDecomposition = `{"elements":[
	{"key":"db","title":"Database changes","goal":"Add SSO tables","type":"feature","agentRole":"backend","dependsOn":[]},
	{"key":"oauth-backend","title":"OAuth backend","goal":"Implement OAuth flow","type":"feature","agentRole":"backend","dependsOn":["db"]},
	{"key":"admin-config","title":"Admin configuration","goal":"Let admins configure SSO","type":"feature","agentRole":"backend","dependsOn":["oauth-backend"]},
	{"key":"frontend","title":"Frontend","goal":"Add SSO login button","type":"feature","agentRole":"frontend","dependsOn":["oauth-backend"]},
	{"key":"docs","title":"Documentation","goal":"Document SSO setup","type":"documentation","agentRole":"docs","dependsOn":["admin-config","frontend"]},
	{"key":"integration-tests","title":"Integration tests","goal":"Cover the SSO flow end to end","type":"feature","agentRole":"qa","dependsOn":["docs"]}
]}`

var ssoRoles = []string{"backend", "frontend", "docs", "qa"}

func TestDecomposeProducesOrderedSubtasks(t *testing.T) {
	model := &stubPlannerModel{body: ssoDecomposition}
	p := swarm.NewPlanner(model, nil, ssoRoles)

	epic := work.Item{Key: "NF-1", Type: "feature", Goal: "Add enterprise SSO"}
	subs, err := p.Decompose(context.Background(), epic, swarm.Bundle{})
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if len(subs) != 6 {
		t.Fatalf("want 6 subtasks, got %d", len(subs))
	}

	var oauth *swarm.Subtask
	for i := range subs {
		if subs[i].Key == "oauth-backend" {
			oauth = &subs[i]
		}
	}
	if oauth == nil {
		t.Fatal("want a subtask keyed \"oauth-backend\"")
	}
	found := false
	for _, d := range oauth.DependsOn {
		if d == "db" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want oauth-backend to depend on db, got dependsOn=%v", oauth.DependsOn)
	}
}

func TestMaterialiseCreatesChildWorkItems(t *testing.T) {
	store := newWorkStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	epic, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "Add enterprise SSO"})
	if err != nil {
		t.Fatalf("Create epic: %v", err)
	}

	model := &stubPlannerModel{body: ssoDecomposition}
	p := swarm.NewPlanner(model, store, ssoRoles)

	subs, err := p.Decompose(ctx, epic, swarm.Bundle{})
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}

	items, err := p.Materialise(ctx, epic, subs)
	if err != nil {
		t.Fatalf("Materialise: %v", err)
	}
	if len(items) != 6 {
		t.Fatalf("want 6 child work items, got %d", len(items))
	}
	for _, item := range items {
		if item.OrgID != orgID {
			t.Fatalf("want child item org %v, got %v", orgID, item.OrgID)
		}
	}

	ready, err := store.Ready(ctx, orgID, epic.ID)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("want exactly 1 dependency-free subtask ready, got %d: %+v", len(ready), ready)
	}
}

func TestUnknownAgentRoleRejected(t *testing.T) {
	body := `{"elements":[
		{"key":"a","title":"A","goal":"do a","type":"feature","agentRole":"mystery-role","dependsOn":[]}
	]}`
	model := &stubPlannerModel{body: body}
	p := swarm.NewPlanner(model, nil, ssoRoles)

	epic := work.Item{Key: "NF-1", Type: "feature", Goal: "epic"}
	_, err := p.Decompose(context.Background(), epic, swarm.Bundle{})
	if err == nil {
		t.Fatal("want error for unknown agent role")
	}
	if !strings.Contains(err.Error(), "unknown agent role") {
		t.Fatalf("want error containing %q, got %q", "unknown agent role", err.Error())
	}
}

func TestDecompositionCycleRejected(t *testing.T) {
	body := `{"elements":[
		{"key":"a","title":"A","goal":"do a","type":"feature","agentRole":"backend","dependsOn":["b"]},
		{"key":"b","title":"B","goal":"do b","type":"feature","agentRole":"backend","dependsOn":["a"]}
	]}`
	model := &stubPlannerModel{body: body}
	p := swarm.NewPlanner(model, nil, ssoRoles)

	epic := work.Item{Key: "NF-1", Type: "feature", Goal: "epic"}
	_, err := p.Decompose(context.Background(), epic, swarm.Bundle{})
	if err == nil {
		t.Fatal("want error for cyclic decomposition")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want error containing %q, got %q", "cycle", err.Error())
	}
}

func TestMaterialiseIsIdempotent(t *testing.T) {
	store := newWorkStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	epic, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "Add enterprise SSO"})
	if err != nil {
		t.Fatalf("Create epic: %v", err)
	}

	model := &stubPlannerModel{body: ssoDecomposition}
	p := swarm.NewPlanner(model, store, ssoRoles)

	subs, err := p.Decompose(ctx, epic, swarm.Bundle{})
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}

	first, err := p.Materialise(ctx, epic, subs)
	if err != nil {
		t.Fatalf("Materialise (1st): %v", err)
	}
	second, err := p.Materialise(ctx, epic, subs)
	if err != nil {
		t.Fatalf("Materialise (2nd): %v", err)
	}
	if len(first) != 6 || len(second) != 6 {
		t.Fatalf("want 6 items from each call, got %d and %d", len(first), len(second))
	}

	firstIDs := make(map[uuid.UUID]bool, len(first))
	for _, item := range first {
		firstIDs[item.ID] = true
	}
	for _, item := range second {
		if !firstIDs[item.ID] {
			t.Fatalf("2nd Materialise produced a new item %s not created by the 1st call: idempotency violated", item.ID)
		}
	}
}

// recordingModel captures the system prompt it was called with, so a test
// can assert what the planner actually told the model.
type recordingModel struct {
	stubPlannerModel
	system string
}

func (m *recordingModel) Generate(ctx context.Context, call provider.Call) (*provider.Response, error) {
	for _, msg := range call.Messages {
		if msg.Role == provider.RoleSystem {
			for _, part := range msg.Content {
				if tp, ok := part.(provider.TextPart); ok {
					m.system += tp.Text
				}
			}
		}
	}
	return m.stubPlannerModel.Generate(ctx, call)
}

// TestDecomposePromptNamesTheLegalRoles pins a defect that made decomposition
// impossible in practice: the planner refuses any subtask naming a role it
// does not know, but never told the model which roles those were, so the
// model guessed and every decomposition was rejected. The role list must
// reach the model.
func TestDecomposePromptNamesTheLegalRoles(t *testing.T) {
	model := &recordingModel{stubPlannerModel: stubPlannerModel{body: ssoDecomposition}}
	p := swarm.NewPlanner(model, nil, ssoRoles)

	if _, err := p.Decompose(context.Background(), work.Item{Key: "NF-1", Goal: "Enterprise SSO"}, swarm.Bundle{}); err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	for _, role := range ssoRoles {
		if !strings.Contains(model.system, role) {
			t.Fatalf("the system prompt never names the legal role %q:\n%s", role, model.system)
		}
	}
}

// TestDefaultAgentRolesCoverADecomposition pins that a repository which has
// declared no agents of its own can still be decomposed: the default roles
// must accept a decomposition that uses them.
func TestDefaultAgentRolesCoverADecomposition(t *testing.T) {
	const body = `{"elements":[
		{"key":"design","title":"Design","goal":"Design it","type":"architecture","agentRole":"architect","dependsOn":[]},
		{"key":"build","title":"Build","goal":"Build it","type":"feature","agentRole":"implementer","dependsOn":["design"]},
		{"key":"docs","title":"Docs","goal":"Document it","type":"documentation","agentRole":"documentation","dependsOn":["build"]}
	]}`
	p := swarm.NewPlanner(&stubPlannerModel{body: body}, nil, swarm.DefaultAgentRoles)

	subs, err := p.Decompose(context.Background(), work.Item{Key: "NF-2", Goal: "Anything"}, swarm.Bundle{})
	if err != nil {
		t.Fatalf("Decompose with the default roles: %v", err)
	}
	if len(subs) != 3 {
		t.Fatalf("want 3 subtasks, got %d", len(subs))
	}
}

// retryModel answers the first call with bad and every later call with
// good, so a test can pin that a rejected decomposition is retried once
// with the violation quoted back.
type retryModel struct {
	stubPlannerModel
	bad, good string
	calls     int
	systems   []string
}

func (m *retryModel) Generate(ctx context.Context, call provider.Call) (*provider.Response, error) {
	for _, msg := range call.Messages {
		if msg.Role == provider.RoleSystem {
			for _, part := range msg.Content {
				if tp, ok := part.(provider.TextPart); ok {
					m.systems = append(m.systems, tp.Text)
				}
			}
		}
	}
	m.calls++
	body := m.good
	if m.calls == 1 {
		body = m.bad
	}
	return &provider.Response{
		Content:      []provider.ContentPart{provider.TextPart{Text: body}},
		FinishReason: provider.FinishStop,
	}, nil
}

// TestDecomposeRejectsAnIllegalType pins a failure seen against a real
// model: it put an agent role ("test") in the "type" field, and the whole
// decomposition was lost at insert time, after some children had been
// written. The type is now checked before anything is written.
func TestDecomposeRejectsAnIllegalType(t *testing.T) {
	const bad = `{"elements":[
		{"key":"a","title":"A","goal":"g","type":"test","agentRole":"backend","dependsOn":[]}
	]}`
	p := swarm.NewPlanner(&stubPlannerModel{body: bad}, nil, ssoRoles)

	_, err := p.Decompose(context.Background(), work.Item{Key: "NF-1", Goal: "x"}, swarm.Bundle{})
	if err == nil {
		t.Fatal("want an error for a subtask whose type is an agent role")
	}
	if !strings.Contains(err.Error(), "test") {
		t.Fatalf("the error does not name the offending type: %v", err)
	}
}

// TestDecomposeRetriesOnceWithTheViolation pins that a single bad field
// does not discard an otherwise sound decomposition: the model is told what
// it got wrong and asked once more.
func TestDecomposeRetriesOnceWithTheViolation(t *testing.T) {
	const bad = `{"elements":[
		{"key":"a","title":"A","goal":"g","type":"test","agentRole":"backend","dependsOn":[]}
	]}`
	const good = `{"elements":[
		{"key":"a","title":"A","goal":"g","type":"feature","agentRole":"backend","dependsOn":[]}
	]}`
	m := &retryModel{bad: bad, good: good}
	p := swarm.NewPlanner(m, nil, ssoRoles)

	subs, err := p.Decompose(context.Background(), work.Item{Key: "NF-1", Goal: "x"}, swarm.Bundle{})
	if err != nil {
		t.Fatalf("Decompose after a corrected retry: %v", err)
	}
	if len(subs) != 1 || subs[0].Type != "feature" {
		t.Fatalf("want the corrected decomposition, got %+v", subs)
	}
	if m.calls != 2 {
		t.Fatalf("want exactly one retry (2 calls), got %d", m.calls)
	}
	if len(m.systems) < 2 || !strings.Contains(m.systems[1], "rejected") {
		t.Fatalf("the retry did not quote the violation back: %q", m.systems)
	}
}

// TestSystemPromptNamesTheLegalTypes pins that the model is told which
// types it may choose, read from the package that owns them.
func TestSystemPromptNamesTheLegalTypes(t *testing.T) {
	m := &recordingModel{stubPlannerModel: stubPlannerModel{body: ssoDecomposition}}
	p := swarm.NewPlanner(m, nil, ssoRoles)

	if _, err := p.Decompose(context.Background(), work.Item{Key: "NF-1", Goal: "x"}, swarm.Bundle{}); err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	for _, typ := range work.SortedTypes() {
		if !strings.Contains(m.system, typ) {
			t.Fatalf("the system prompt never names the legal type %q", typ)
		}
	}
}
