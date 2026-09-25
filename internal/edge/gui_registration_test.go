package edge

import (
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"testing"
)

func TestGUIProductionRegistration(t *testing.T) {
	cfg := Config{Git: gitv1.NewGitServiceClient(nil), Work: workv1.NewWorkServiceClient(nil), Reviews: reviewsv1.NewReviewsServiceClient(nil), Agents: agentsv1.NewAgentServiceClient(nil)}
	h := Handlers(cfg)
	for _, op := range []string{"patchWorkItem", "transitionWorkItem", "listRunReviews", "listEngineeringRunReviews", "getAgentRunTools", "streamAgentRunEvents"} {
		if h[op] == nil {
			t.Errorf("production handler missing: %s", op)
		}
		found := false
		for _, r := range Routes() {
			if r.OpID == op {
				found = true
			}
		}
		if !found {
			t.Errorf("production route missing: %s", op)
		}
	}
	// Known run identity must survive absence of the Git client itself.
	if Handlers(Config{Reviews: cfg.Reviews})["listEngineeringRunReviews"] == nil {
		t.Error("canonical history depends on Git")
	}
}
