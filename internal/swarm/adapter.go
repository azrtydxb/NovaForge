package swarm

import (
	"context"
	"fmt"

	"github.com/novaforge/novaforge/internal/work"
)

// PlannerDecomposer adapts the Planner to work.Decomposer, so the work service
// can offer decomposition without depending on a model client.
type PlannerDecomposer struct {
	Planner *Planner
}

// DecomposeAndMaterialise asks the model for subtasks and records them.
//
// The two steps are joined here because a decomposition that is never written
// is not a decomposition: the caller wants the Work Items, not the model's
// answer.
func (d PlannerDecomposer) DecomposeAndMaterialise(ctx context.Context, epic work.Item) ([]work.Item, error) {
	if d.Planner == nil || d.Planner.Model == nil {
		return nil, fmt.Errorf("no model is configured for this deployment")
	}
	subs, err := d.Planner.Decompose(ctx, epic, Bundle{})
	if err != nil {
		return nil, err
	}
	return d.Planner.Materialise(ctx, epic, subs)
}
