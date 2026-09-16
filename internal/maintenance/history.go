package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
)

// ciJobResults reads evidence through CI's org-scoped RPCs, never its schema.
// Only explicit Go test events name individual test outcomes. An arbitrary
// command's exit status, or a package-level failure, cannot establish flakiness.
func ciJobResults(ctx context.Context, client civ1.CIServiceClient, repoID uuid.UUID, branch string) ([]JobResult, error) {
	if client == nil {
		return nil, fmt.Errorf("CI history is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	runs, err := client.ListRuns(ctx, &civ1.ListRunsRequest{RepoId: repoID.String()})
	if err != nil {
		return nil, fmt.Errorf("list CI history: %w", err)
	}
	// debt: CI ListRuns has no pagination, so listing still reads all metadata.
	// Only the latest 100 runs are inspected; paginate when CI exposes cursors.
	recent := runs.GetRuns()
	if len(recent) > 100 {
		recent = recent[:100]
	}
	var results []JobResult
	for _, run := range recent {
		if run.GetRepoId() != repoID.String() || run.GetCommitSha() == "" ||
			strings.TrimPrefix(run.GetRef(), "refs/heads/") != strings.TrimPrefix(branch, "refs/heads/") {
			continue
		}
		detail, err := client.GetRun(ctx, &civ1.GetRunRequest{Id: run.GetId()})
		if err != nil {
			return nil, fmt.Errorf("read CI run %s: %w", run.GetId(), err)
		}
		for _, job := range detail.GetJobs() {
			if job.GetAgentRole() != "" || (job.GetStatus() != "success" && job.GetStatus() != "failure") {
				continue
			}
			logs, err := client.GetJobLogs(ctx, &civ1.GetJobLogsRequest{JobId: job.GetId()})
			if err != nil {
				// Do not report partial history as a successful check.
				return nil, fmt.Errorf("read CI job %s: %w", job.GetId(), err)
			}
			results = append(results, goTestResults(logs.GetLines(), job.GetName(), run.GetCommitSha())...)
		}
	}
	return results, nil
}

func goTestResults(chunks []string, job, sha string) []JobResult {
	var results []JobResult
	for _, chunk := range chunks {
		for _, line := range strings.Split(chunk, "\n") {
			var event struct{ Action, Package, Test string }
			if json.Unmarshal([]byte(line), &event) != nil || event.Package == "" || event.Test == "" ||
				(event.Action != "pass" && event.Action != "fail") {
				continue
			}
			results = append(results, JobResult{
				TestName:  job + ": " + event.Package + "/" + event.Test,
				CommitSHA: sha, Passed: event.Action == "pass",
			})
		}
	}
	return results
}
