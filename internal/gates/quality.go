package gates

import (
	"context"
	"strings"

	"github.com/novaforge/novaforge/internal/analysis"
)

// runQuality evaluates the quality gate: `go vet` clean and every file
// gofmt-formatted.
func runQuality(ctx context.Context, in Input) (Evaluation, error) {
	if !analysis.IsGoModule(in.WorkDir) {
		return notGo(in, "quality")
	}
	res, err := analysis.Quality(ctx, in.Exec, in.WorkDir)
	if err != nil {
		return toolError(in, "quality", err)
	}
	if res.Passed() {
		return newEvaluation(in, "quality", "pass", "go vet clean, gofmt clean"), nil
	}
	var b strings.Builder
	if res.VetOutput != "" {
		b.WriteString("go vet:\n" + res.VetOutput + "\n")
	}
	if len(res.Unformatted) > 0 {
		b.WriteString("not gofmt-formatted:\n" + strings.Join(res.Unformatted, "\n"))
	}
	return newEvaluation(in, "quality", "fail", strings.TrimRight(b.String(), "\n")), nil
}
