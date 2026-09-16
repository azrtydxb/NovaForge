package maintenance

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
)

// ciBenchmarkResults compares each latest measurement to its nearest earlier
// successful, comparable default-branch run. CI ListRuns orders newest first;
// do not re-sort its second-resolution timestamps and lose database ordering.
func ciBenchmarkResults(ctx context.Context, client civ1.CIServiceClient, repo uuid.UUID, branch string) ([]BenchmarkResult, error) {
	if client == nil {
		return nil, fmt.Errorf("benchmark history unavailable: CI is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	runs, err := client.ListRuns(ctx, &civ1.ListRunsRequest{RepoId: repo.String()})
	if err != nil {
		return nil, fmt.Errorf("list benchmark history: %w", err)
	}
	// debt: inspect at most 100 runs until CI exposes paginated metadata;
	// the current ListRuns RPC still returns the entire repository history.
	recent := runs.GetRuns()
	if len(recent) > 100 {
		recent = recent[:100]
	}
	var latest map[string]benchmarkReport
	var latestID string
	matched := map[string]map[string]bool{}
	var results []BenchmarkResult
	var unavailable []error
	for _, run := range recent {
		if run.GetRepoId() != repo.String() || run.GetStatus() != "success" || run.GetCommitSha() == "" ||
			strings.TrimPrefix(run.GetRef(), "refs/heads/") != strings.TrimPrefix(branch, "refs/heads/") {
			continue
		}
		reports, err := benchmarkArtifacts(ctx, client, run.GetId())
		if err != nil {
			// Lost evidence could hide the nearest comparable baseline. Do
			// not skip a read failure and silently choose a more distant one.
			return nil, fmt.Errorf("benchmark run %s unavailable: %w", run.GetId(), err)
		}
		if latest == nil {
			if len(reports) == 0 {
				return nil, fmt.Errorf("latest successful default-branch run %s has no benchmarks.txt evidence", run.GetId())
			}
			latest, latestID = reports, run.GetId()
			for job := range latest {
				matched[job] = map[string]bool{}
			}
			continue
		}
		for job, current := range latest {
			previous, ok := reports[job]
			if !ok || previous.Context != current.Context {
				continue
			}
			for metric, value := range current.Values {
				baseline, exists := previous.Values[metric]
				if !exists || matched[job][metric] {
					continue
				}
				matched[job][metric] = true
				if baseline <= 0 {
					// A zero allocation baseline cannot produce a percentage.
					// Do not replace it with an older, more convenient value.
					unavailable = append(unavailable, fmt.Errorf("benchmark %s/%s percentage unavailable: baseline is zero", job, metric))
					continue
				}
				results = append(results, BenchmarkResult{
					Name:     job + ": " + current.Context[5] + "/" + metric,
					Baseline: baseline, Latest: value,
					BaselineRun: run.GetId(), LatestRun: latestID,
				})
			}
		}
		if benchmarksMatched(latest, matched) {
			sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
			return results, errors.Join(unavailable...)
		}
	}
	unavailable = append(unavailable, fmt.Errorf("benchmark comparison unavailable: no complete comparable successful default-branch baseline in latest 100 runs"))
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	return results, errors.Join(unavailable...)
}

func benchmarksMatched(latest map[string]benchmarkReport, matched map[string]map[string]bool) bool {
	for job, report := range latest {
		if len(matched[job]) != len(report.Values) {
			return false
		}
	}
	return true
}
