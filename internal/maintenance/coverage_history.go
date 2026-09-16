package maintenance

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
)

func gateCoverage(ctx context.Context, client gatesv1.GatesServiceClient, repo uuid.UUID) (CoverageSample, error) {
	if client == nil {
		return CoverageSample{}, fmt.Errorf("coverage unavailable: gates client is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	history, err := client.ListCoverageHistory(ctx, &gatesv1.ListCoverageHistoryRequest{RepoId: repo.String()})
	if err != nil {
		return CoverageSample{}, fmt.Errorf("read coverage history: %w", err)
	}
	return coverageEvidence(history.GetEvaluations(), repo)
}

func coverageEvidence(evals []*gatesv1.Evaluation, repo uuid.UUID) (CoverageSample, error) {
	if len(evals) != 2 {
		return CoverageSample{}, fmt.Errorf("coverage comparison unavailable: need two recorded head evaluations")
	}
	for _, e := range evals {
		if e.GetRepoId() != repo.String() || e.GetGate() != "tests" || e.GetTargetSha() == "" ||
			(e.GetStatus() != "pass" && e.GetStatus() != "fail") || e.CoveragePercent == nil {
			return CoverageSample{}, fmt.Errorf("coverage unavailable for evaluation %s", e.GetId())
		}
		p := e.GetCoveragePercent()
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 100 {
			return CoverageSample{}, fmt.Errorf("invalid coverage evidence")
		}
	}
	return CoverageSample{Previous: evals[1].CoveragePercent, Latest: evals[0].CoveragePercent,
		Evidence: fmt.Sprintf("previous evaluation %s at %s; latest evaluation %s at %s", evals[1].GetId(), evals[1].GetTargetSha(), evals[0].GetId(), evals[0].GetTargetSha()),
	}, nil
}
