package gates

import (
	"context"
	"fmt"

	"github.com/novaforge/novaforge/internal/analysis"
)

// runDocs evaluates the documentation gate: exported Go declarations carry a
// doc comment. max_undocumented (default 0) lets a repository adopt the gate
// before it has paid down an existing backlog, rather than having to switch
// the gate off to merge anything.
func runDocs(ctx context.Context, in Input) (Evaluation, error) {
	if !analysis.IsGoModule(in.WorkDir) {
		return notGo(in, "documentation")
	}
	found, err := analysis.UndocumentedExports(in.WorkDir)
	if err != nil {
		return toolError(in, "documentation", err)
	}
	limit := int(paramFloat(in.Params, "max_undocumented", 0))
	if len(found) <= limit {
		return newEvaluation(in, "documentation", "pass",
			fmt.Sprintf("%d undocumented exported declarations (limit %d)", len(found), limit)), nil
	}
	return newEvaluation(in, "documentation", "fail",
		fmt.Sprintf("%d undocumented exported declarations, limit %d:\n%s", len(found), limit, list(found))), nil
}
