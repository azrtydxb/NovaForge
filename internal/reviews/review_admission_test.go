package reviews

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"google.golang.org/grpc"
)

type admissionGit struct{ gitv1.GitServiceClient }

func (admissionGit) ListCommits(context.Context, *gitv1.ListCommitsRequest, ...grpc.CallOption) (*gitv1.ListCommitsResponse, error) {
	return &gitv1.ListCommitsResponse{Commits: []*gitv1.Commit{{Sha: strings.Repeat("a", 40)}}}, nil
}
func (admissionGit) GetDiff(context.Context, *gitv1.GetDiffRequest, ...grpc.CallOption) (*gitv1.GetDiffResponse, error) {
	return &gitv1.GetDiffResponse{Unified: "+change"}, nil
}

type admissionAgents struct {
	agentsv1.AgentServiceClient
	agent *agentsv1.Agent
}

func (a admissionAgents) ListAgents(context.Context, *agentsv1.ListAgentsRequest, ...grpc.CallOption) (*agentsv1.ListAgentsResponse, error) {
	return &agentsv1.ListAgentsResponse{Agents: []*agentsv1.Agent{a.agent}}, nil
}

func TestReviewAdmissionSurvivesUncertaintyAndWorkerRestart(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "late_completed_response", true: "cancelled_lost_then_late_completion"}[unknown], func(t *testing.T) {
			store := executionTestStore(t)
			pool := store.pool
			org := uuid.New()
			ctx := executionScope(org)
			config := ReviewConfig{WallclockSeconds: 60, MaxInputBytes: 4096, MaxOutputTokens: 128, MaxConcurrentRequests: 1, MaxRoles: 1}
			enqueue := func() uuid.UUID {
				t.Helper()
				run, err := store.CreateRun(ctx, Run{OrgID: org, RepoID: uuid.New(), Title: "review", SourceRef: "feature", TargetRef: "main", AuthorID: uuid.New(), AuthorKind: "user"})
				if err != nil {
					t.Fatal(err)
				}
				id := uuid.New()
				if _, err = pool.Exec(ctx, `INSERT INTO reviews.agent_review_requests(id,org_id,run_id,requested_by,source_sha,target_sha,state) VALUES($1,$2,$3,$4,$5,$5,'queued')`, id, org, run.ID, uuid.New(), strings.Repeat("a", 40)); err != nil {
					t.Fatal(err)
				}
				return id
			}
			first, second := enqueue(), enqueue()
			owner := executionOwner(t, store)
			owner.entered = make(chan struct{}, 1)
			owner.release = make(chan struct{})
			if unknown {
				owner.modes("lost", "")
			}
			defer func() {
				select {
				case <-owner.release:
				default:
					close(owner.release)
				}
			}()
			gateway := owner.gateway(t, store, "original-principal")
			worker := &ReviewWorker{Store: store, Gateway: gateway, Git: admissionGit{}, Agents: admissionAgents{agent: &agentsv1.Agent{Id: uuid.NewString(), OrgId: org.String(), Role: "reviewer", Enabled: true}}, Config: config, HMACSecret: "review-admission-test"}
			workerCtx, cancelWorker := context.WithCancel(context.Background())
			defer cancelWorker()
			done := make(chan error, 1)
			go func() { done <- worker.Tick(workerCtx) }()
			select {
			case <-owner.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("model not entered")
			}
			var attempt uuid.UUID
			if err := pool.QueryRow(ctx, `SELECT attempt_id FROM reviews.review_executions WHERE request_id=$1`, first).Scan(&attempt); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE reviews.agent_review_requests SET lease_until=now()-interval '1 second' WHERE id=$1 AND org_id=$2`, first, org); err != nil {
				t.Fatal(err)
			}
			if unknown {
				cancelWorker()
			}
			restarted := *worker
			restarted.Store = NewStore(pool)
			restarted.Gateway = owner.gateway(t, restarted.Store, "original-principal")
			if err := restarted.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if calls, _ := owner.counts(); calls != 1 {
				t.Fatalf("lease expiry admitted another model: %d", calls)
			}
			var state string
			if err := pool.QueryRow(ctx, `SELECT state FROM reviews.agent_review_requests WHERE id=$1 AND org_id=$2`, second, org).Scan(&state); err != nil || state != "queued" {
				t.Fatalf("second request escaped admission: %s %v", state, err)
			}
			if _, err := pool.Exec(ctx, `DELETE FROM reviews.agent_review_requests WHERE id=$1 AND org_id=$2`, first, org); err != nil {
				t.Fatal(err)
			}
			if err := restarted.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if calls, _ := owner.counts(); calls != 1 {
				t.Fatal("history purge released live execution")
			}
			close(owner.release)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("original worker did not return")
			}
			if unknown {
				if err := restarted.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				executionHeld(t, store, attempt, true)
				if calls, _ := owner.counts(); calls != 1 {
					t.Fatal("cancellation or lost response freed admission")
				}
				owner.complete(attempt)
			}
			// A late receipt may retire admission, not turn the stale/deleted first
			// request into an approval or synthesize usage from a body it never saw.
			deadline := time.Now().Add(5 * time.Second)
			for {
				owner.mu.Lock()
				complete := owner.receipts[attempt.String()].Terminal
				owner.mu.Unlock()
				if complete {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("late response not observed")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := restarted.Gateway.reconcileReviewExecutions(ctx); err != nil {
				t.Fatal(err)
			}
			executionHeld(t, store, attempt, false)
			var reviews int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM reviews.run_reviews`).Scan(&reviews); err != nil || reviews != 0 {
				t.Fatalf("late receipt fabricated approval: %d %v", reviews, err)
			}
			owner.modes("", "")
			if err := restarted.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if calls, _ := owner.counts(); calls != 2 {
				t.Fatalf("new request not admitted after terminal proof: %d", calls)
			}
		})
	}
}
