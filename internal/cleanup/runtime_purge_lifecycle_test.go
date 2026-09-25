package cleanup_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/agentrun"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/platformtest"
	"github.com/novaforge/novaforge/internal/tools"
)

// Only inference is controlled. Unlike platformtest.ControlledRuns, this fixture
// leaves settlement to the deletion handler: an expected pending audit must not
// be turned into a test error by automatic SettleRun. Runner still persists its
// own completion. There is no workspace/provider execution in this fixture.
type runtimePurgeModel struct {
	ready, release chan struct{}
}

func (m *runtimePurgeModel) Generate(ctx context.Context, _ provider.Call) (*provider.Response, error) {
	close(m.ready)
	// The cancellation watcher may fire while purge contacts Identity. Keep
	// return independently controlled: cancelled is not execution-finished.
	<-m.release
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*runtimePurgeModel) Stream(context.Context, provider.Call) (provider.StreamResponse, error) {
	return nil, fmt.Errorf("purge fixture uses Generate")
}
func (*runtimePurgeModel) ModelID() string                     { return "controlled-inference" }
func (*runtimePurgeModel) ProviderName() string                { return "fixture" }
func (*runtimePurgeModel) Capabilities() provider.Capabilities { return provider.Capabilities{} }

type runtimePurgeExecution struct {
	id             uuid.UUID
	ready, release chan struct{}
	done           chan struct{}
	cancel         context.CancelFunc
	stopOnce       sync.Once
	err            error // read only after done closes
}

func (e *runtimePurgeExecution) stop(t *testing.T) {
	t.Helper()
	e.stopOnce.Do(func() {
		e.cancel()
		close(e.release)
	})
	select {
	case <-e.done:
		if e.err != nil {
			t.Errorf("Runner completion: %v", e.err)
		}
	case <-time.After(15 * time.Second):
		t.Error("Runner did not return after cancellation")
	}
}

func runtimePurgePlatform(t *testing.T) (*platformtest.Platform, <-chan *runtimePurgeExecution) {
	t.Helper()
	started := make(chan *runtimePurgeExecution, 3)
	p := platformtest.StartWithExecutor(t, func(p *platformtest.Platform) agents.ExecuteFunc {
		return func(ctx context.Context, run agents.Run) {
			ctx, cancel := context.WithCancel(ctx)
			e := &runtimePurgeExecution{id: run.ID, ready: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{}), cancel: cancel}
			// Register before publishing the execution, including if StartRun's
			// reply or the inference barrier subsequently fails.
			t.Cleanup(func() { e.stop(t) })
			started <- e
			defer close(e.done)
			defer cancel()
			ctx, e.err = agentrun.WithRunIdentity(ctx, platformtest.HMACSecret, run.OrgID, run.AgentID, agentrun.RunCredentialTTL(run))
			if e.err != nil {
				return
			}
			authority := capability.RuntimeClient{Identity: p.Identity, HMACSecret: platformtest.HMACSecret}
			grant, err := authority.Resolve(ctx, run.GrantID)
			if err != nil {
				e.err = err
				return
			}
			runner := agentrun.Runner{Git: p.Git, Work: p.Work, Runs: p.AgentStore, Audit: agents.NewAuditLog(p.Pool),
				NewModel: func(string) (provider.LanguageModel, error) {
					return &runtimePurgeModel{ready: e.ready, release: e.release}, nil
				}}
			result := runner.Run(ctx, run, tools.Runtime{Grant: grant, Work: tools.NewWorkClient(p.Work)})
			e.err = result.PersistenceError
		}
	})
	return p, started
}

type runtimePurgeRun struct {
	id, org, repo, agent, audit uuid.UUID
	ctx                         context.Context
	execution                   *runtimePurgeExecution
}

func newRuntimePurgeRun(t *testing.T, p *platformtest.Platform, started <-chan *runtimePurgeExecution, user platformtest.User, org platformtest.Org, agent string) runtimePurgeRun {
	t.Helper()
	reply, err := p.Git.CreateRepo(p.AsUser(user, org), &gitv1.CreateRepoRequest{Name: "purge-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatal(err)
	}
	repo := platformtest.Repo{ID: reply.GetRepo().GetId(), Name: reply.GetRepo().GetName()}
	if _, err := p.Git.CreateCommit(p.AsUser(user, org), &gitv1.CreateCommitRequest{
		Repo: repo.ID, Branch: "main", Message: "fixture", Files: []*gitv1.FileChange{{Path: "README.md", Content: []byte("fixture\n")}},
	}); err != nil {
		t.Fatal(err)
	}
	item := p.NewWorkItem(t, user, org, repo)
	run := p.StartAgentRun(t, user, org, repo, agent, item)
	f := runtimePurgeRun{id: uuid.MustParse(run.GetId()), org: uuid.MustParse(org.ID), repo: uuid.MustParse(repo.ID), agent: uuid.MustParse(agent)}
	select {
	case f.execution = <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("executor did not start")
	}
	if f.execution.id != f.id {
		t.Fatal("unexpected execution identity")
	}
	select {
	case <-f.execution.ready:
	case <-f.execution.done:
		t.Fatalf("Runner ended before inference: %v", f.execution.err)
	case <-time.After(10 * time.Second):
		t.Fatal("Runner did not reach inference")
	}
	f.ctx = authz.WithScope(context.Background(), authz.Scope{OrgID: f.org, ActorID: f.agent, ActorKind: "agent"})
	// An attempted call reserved in the durable audit, explicitly NOT dispatched.
	// Its later refused receipt claims neither a Git commit nor provider cleanup.
	f.audit, err = agents.NewAuditLog(p.Pool).Record(f.ctx, agents.Entry{RunID: f.id, Tool: "git.commit", ArgsJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f runtimePurgeRun) saved(t *testing.T, p *platformtest.Platform) agents.Run {
	t.Helper()
	run, err := p.AgentStore.GetRun(f.ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func (f runtimePurgeRun) refuseUndispatchedCall(t *testing.T, p *platformtest.Platform) {
	t.Helper()
	if err := agents.NewAuditLog(p.Pool).Complete(f.ctx, f.audit, "refused", ""); err != nil {
		t.Fatal(err)
	}
}

func (f runtimePurgeRun) assertClaim(t *testing.T, p *platformtest.Platform, released bool) {
	t.Helper()
	var matches bool
	if err := p.Pool.QueryRow(f.ctx, `SELECT CASE WHEN $3 THEN c.outcome='cancelled' AND c.released_at IS NOT NULL AND w.active_execution_run_id IS NULL
 ELSE c.outcome IS NULL AND c.released_at IS NULL AND w.active_execution_run_id=c.run_id END
 FROM work.execution_claims c JOIN work.work_items w ON w.org_id=c.org_id AND w.id=c.work_item_id
 WHERE c.org_id=$1 AND c.run_id=$2`, f.org, f.id, released).Scan(&matches); err != nil {
		t.Fatal(err)
	}
	if !matches {
		t.Fatalf("Work owner claim does not match released=%v", released)
	}
}

// Read the actual Identity-owned durable receipt as well as the runtime's ack.
// A missing grant is not accepted as proof of fencing or termination.
func (f runtimePurgeRun) assertGrant(t *testing.T, p *platformtest.Platform, cancelled bool) {
	t.Helper()
	var matches bool
	if err := p.Pool.QueryRow(f.ctx, `SELECT i.cancelled=$3 AND (g.expires_at<=now())=$3
 FROM gitplatform.capability_issuances i JOIN gitplatform.capability_grants g ON g.org_id=i.org_id AND g.id=i.id
 WHERE i.org_id=$1 AND i.run_id=$2`, f.org, f.id, cancelled).Scan(&matches); err != nil {
		t.Fatal(err)
	}
	if !matches {
		t.Fatalf("Identity grant receipt does not match cancelled=%v", cancelled)
	}
}
