package platformtest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/agentrun"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/tools"
)

// ControlledRuns executes the real Runner with only inference held at a barrier.
// It provides live-authority integration evidence, NOT Kubernetes process proof.
// Cancelling the model drives the real completion, owner fencing and Work release.
type ControlledRuns struct {
	mu   sync.Mutex
	runs map[uuid.UUID]*controlledRun
}
type controlledRun struct {
	ready, done chan struct{}
	cancel      context.CancelFunc
}

func (c *ControlledRuns) entry(id uuid.UUID) *controlledRun {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.runs[id]
	if r == nil {
		r = &controlledRun{ready: make(chan struct{}), done: make(chan struct{})}
		c.runs[id] = r
	}
	return r
}
func (c *ControlledRuns) Wait(t testing.TB, id string) {
	t.Helper()
	r := c.entry(uuid.MustParse(id))
	select {
	case <-r.ready:
	case <-r.done:
		t.Fatal("Runner ended before inference barrier")
	case <-time.After(10 * time.Second):
		t.Fatal("Runner did not reach inference barrier")
	}
}
func (c *ControlledRuns) Stop(t testing.TB, id string) {
	t.Helper()
	r := c.entry(uuid.MustParse(id))
	c.mu.Lock()
	cancel := r.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-r.done:
	case <-time.After(15 * time.Second):
		t.Fatal("Runner did not settle after cancellation")
	}
}
func StartWithControlledRunner(t testing.TB) (*Platform, *ControlledRuns) {
	t.Helper()
	control := &ControlledRuns{runs: make(map[uuid.UUID]*controlledRun)}
	p := StartWithExecutor(t, func(p *Platform) agents.ExecuteFunc {
		return func(ctx context.Context, run agents.Run) {
			r := control.entry(run.ID)
			ctx, cancel := context.WithCancel(ctx)
			control.mu.Lock()
			r.cancel = cancel
			control.mu.Unlock()
			defer close(r.done)
			defer cancel()
			ctx, err := agentrun.WithRunIdentity(ctx, HMACSecret, run.OrgID, run.AgentID, agentrun.RunCredentialTTL(run))
			if err != nil {
				t.Error(err)
				return
			}
			authority := capability.RuntimeClient{Identity: p.Identity, HMACSecret: HMACSecret}
			grant, err := authority.Resolve(ctx, run.GrantID)
			if err != nil {
				t.Error(err)
				return
			}
			runner := agentrun.Runner{Git: p.Git, Work: p.Work, Runs: p.AgentStore, Audit: agents.NewAuditLog(p.Pool), NewModel: func(string) (provider.LanguageModel, error) { return &heldModel{ready: r.ready}, nil }}
			result := runner.Run(ctx, run, tools.Runtime{Grant: grant, Work: tools.NewWorkClient(p.Work)})
			if result.PersistenceError != nil {
				t.Error(result.PersistenceError)
				return
			}
			if err := agents.SettleRun(context.WithoutCancel(ctx), p.AgentStore, p.Redis, run, result.State); err != nil {
				t.Error(err)
			}
		}
	})
	t.Cleanup(func() {
		control.mu.Lock()
		runs := make([]*controlledRun, 0, len(control.runs))
		for _, r := range control.runs {
			runs = append(runs, r)
			if r.cancel != nil {
				r.cancel()
			}
		}
		control.mu.Unlock()
		for _, r := range runs {
			select {
			case <-r.done:
			case <-time.After(15 * time.Second):
				t.Error("controlled Runner cleanup timed out")
			}
		}
	})
	return p, control
}

type heldModel struct {
	ready chan struct{}
	once  sync.Once
}

func (m *heldModel) Generate(ctx context.Context, _ provider.Call) (*provider.Response, error) {
	m.once.Do(func() { close(m.ready) })
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*heldModel) Stream(context.Context, provider.Call) (provider.StreamResponse, error) {
	return nil, fmt.Errorf("controlled model uses Generate")
}
func (*heldModel) ModelID() string                     { return "controlled-inference" }
func (*heldModel) ProviderName() string                { return "fixture" }
func (*heldModel) Capabilities() provider.Capabilities { return provider.Capabilities{} }
