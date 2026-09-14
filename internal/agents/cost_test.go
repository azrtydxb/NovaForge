package agents_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/agents"
)

// TestStartRunRefusesCostLimitWithoutPrice pins that a cost limit is refused
// in a deployment that prices no tokens. Nothing accrued cost, so a cost
// limit was accepted, stored, shown, and could never be reached — a run that
// looked bounded by cost and was not.
func TestStartRunRefusesCostLimitWithoutPrice(t *testing.T) {
	orgID := uuid.New()
	repoID := uuid.New()
	work := &stubWorkClient{item: &workv1.WorkItem{
		Id: uuid.New().String(), Key: "NF-7", RepoId: repoID.String(), State: "open",
	}}
	srv, store := newGRPCServer(t, work)
	ctx := scopedCtx(orgID)
	agent := mustCreateAgent(t, store, ctx, orgID)
	req := &agentsv1.StartRunRequest{
		AgentId: agent.ID.String(), RepoId: repoID.String(), WorkItemKey: "NF-7",
		SponsorId: uuid.New().String(), CostLimitMicros: 1_000_000,
	}

	_, err := srv.StartRun(ctx, req)
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "AI_MODEL_PRICES") {
		t.Fatalf("StartRun with a cost limit and no price = %v, want InvalidArgument naming AI_MODEL_PRICES", err)
	}

	// With a price the same request is accepted and the limit is kept.
	srv.Price = &agents.TokenPrice{InputMicrosPerMillion: 1, OutputMicrosPerMillion: 1}
	resp, err := srv.StartRun(ctx, req)
	if err != nil {
		t.Fatalf("StartRun with a price: %v", err)
	}
	if resp.GetRun().GetCostLimitMicros() != 1_000_000 {
		t.Fatalf("cost limit = %d, want 1000000", resp.GetRun().GetCostLimitMicros())
	}

	// No limit asked for means none — not a default no token could reach.
	req.CostLimitMicros = 0
	srv.Price = nil
	resp, err = srv.StartRun(ctx, req)
	if err != nil {
		t.Fatalf("StartRun without a cost limit: %v", err)
	}
	if resp.GetRun().GetCostLimitMicros() != 0 {
		t.Fatalf("cost limit = %d, want 0 (none)", resp.GetRun().GetCostLimitMicros())
	}
}

func TestTokenPriceRoundsUp(t *testing.T) {
	p := agents.TokenPrice{InputMicrosPerMillion: 150_000, OutputMicrosPerMillion: 600_000}
	// 10 input tokens at 0.15 micros and 1 output at 0.6 micros is 2.1 micros.
	if got := p.CostMicros(10, 1); got != 3 {
		t.Fatalf("CostMicros = %d, want 3 (rounded up)", got)
	}
	if got := p.CostMicros(0, 0); got != 0 {
		t.Fatalf("CostMicros of nothing = %d, want 0", got)
	}
}

func TestParseModelPricesFailsLoudly(t *testing.T) {
	if got, err := agents.ParseModelPrices(""); err != nil || len(got) != 0 {
		t.Fatalf("empty = %v, %v; want no prices", got, err)
	}
	got, err := agents.ParseModelPrices(`{"m":{"input_micros_per_million_tokens":100,"output_micros_per_million_tokens":400}}`)
	if err != nil || got["m"].OutputMicrosPerMillion != 400 {
		t.Fatalf("valid = %v, %v", got, err)
	}
	for _, bad := range []string{
		`{"m":{"input_price":1}}`,
		`{"m":{"input_micros_per_million_tokens":0,"output_micros_per_million_tokens":0}}`,
		`{"m":{"input_micros_per_million_tokens":-1,"output_micros_per_million_tokens":5}}`,
		`not json`,
	} {
		if _, err := agents.ParseModelPrices(bad); err == nil {
			t.Errorf("ParseModelPrices(%s) accepted it", bad)
		}
	}
}

func TestZeroCostLimitMeansNone(t *testing.T) {
	b := agents.NewBudget(3600e9, 1000, 0)
	b.AddCostMicros(1_000_000)
	if err := b.Check(); err != nil {
		t.Fatalf("a run with no cost limit went over budget on cost: %v", err)
	}
}
