package ci

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
)

// dispatchSendTimeout bounds how long Dispatch waits for a slow or stuck
// runner channel before giving up, so a wedged consumer cannot hang the
// dispatch loop forever.
const dispatchSendTimeout = 5 * time.Second

// DispatchJob is the job payload pushed down a runner's Connect stream.
type DispatchJob struct {
	JobID        uuid.UUID
	RunID        uuid.UUID
	RepoCloneURL string
	CommitSHA    string
	RunCmd       string
	AgentRole    string
	Image        string
	Env          map[string]string
	// ArtifactPaths are declared by the workflow; the runner keeps only these.
	ArtifactPaths []string
}

// registeredRunner is one runner currently holding an open Connect stream.
type registeredRunner struct {
	ch           chan<- *civ1.ConnectResponse
	labels       map[string]struct{}
	lastDispatch time.Time
}

// Dispatcher tracks every runner's outbound stream channel and picks a
// matching, least-recently-used runner for each job. Runners hold a
// persistent outbound gRPC stream and the platform pushes jobs down it, so
// a runner never needs inbound network reachability.
type Dispatcher struct {
	mu      sync.Mutex
	runners map[uuid.UUID]*registeredRunner
	store   *Store
}

// NewDispatcher builds a Dispatcher whose reaper marks jobs orphaned by a
// disconnected runner as failed through store. store may be nil in tests
// that only exercise in-memory dispatch and never call Unregister.
func NewDispatcher(store *Store) *Dispatcher {
	return &Dispatcher{runners: make(map[uuid.UUID]*registeredRunner), store: store}
}

// Register records runnerID as connected and able to receive jobs whose
// required labels are a subset of labels, delivered on ch.
func (d *Dispatcher) Register(runnerID uuid.UUID, labels []string, ch chan<- *civ1.ConnectResponse) {
	set := make(map[string]struct{}, len(labels))
	for _, l := range labels {
		set[l] = struct{}{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.runners[runnerID] = &registeredRunner{ch: ch, labels: set}
}

// Unregister removes runnerID from the dispatch pool and reaps any job still
// running against it: a runner that disconnects mid-job — dies, loses
// network, is killed — must never leave that job running forever, so its
// status becomes failure with an explanatory detail instead.
func (d *Dispatcher) Unregister(ctx context.Context, runnerID uuid.UUID) {
	d.mu.Lock()
	_, existed := d.runners[runnerID]
	delete(d.runners, runnerID)
	d.mu.Unlock()

	if !existed || d.store == nil {
		return
	}
	jobIDs, err := d.store.RunningJobsForRunner(ctx, runnerID)
	if err != nil {
		return
	}
	for _, jobID := range jobIDs {
		_ = d.store.SetJobStatus(ctx, jobID, "failure", "runner disconnected")
	}
}

// Dispatch selects the least-recently-dispatched connected runner whose
// labels are a superset of requiredLabels and pushes job down its channel,
// returning an error naming "no runner" when none match.
func (d *Dispatcher) Dispatch(ctx context.Context, job DispatchJob, requiredLabels []string) error {
	d.mu.Lock()
	var (
		bestID uuid.UUID
		best   *registeredRunner
	)
	for id, r := range d.runners {
		if !hasAllLabels(r.labels, requiredLabels) {
			continue
		}
		if best == nil || r.lastDispatch.Before(best.lastDispatch) {
			bestID, best = id, r
		}
	}
	if best == nil {
		d.mu.Unlock()
		return fmt.Errorf("no runner matches labels %v", requiredLabels)
	}
	best.lastDispatch = time.Now()
	ch := best.ch
	d.mu.Unlock()

	msg := toConnectResponse(job)
	select {
	case ch <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(dispatchSendTimeout):
		return fmt.Errorf("dispatch job %s to runner %s: timed out", job.JobID, bestID)
	}
}

func hasAllLabels(have map[string]struct{}, want []string) bool {
	for _, w := range want {
		if _, ok := have[w]; !ok {
			return false
		}
	}
	return true
}

// ConnectedRunner is one runner currently holding an open stream.
type ConnectedRunner struct {
	ID     uuid.UUID
	Labels []string
}

// Connected returns the runners currently able to receive work. The pump asks
// each of them for a job rather than picking a job and hunting for a runner,
// because claiming is per-runner in the database.
func (d *Dispatcher) Connected() []ConnectedRunner {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]ConnectedRunner, 0, len(d.runners))
	for id, r := range d.runners {
		labels := make([]string, 0, len(r.labels))
		for l := range r.labels {
			labels = append(labels, l)
		}
		out = append(out, ConnectedRunner{ID: id, Labels: labels})
	}
	return out
}

// DispatchTo sends job to one specific runner, which is what the pump wants
// once the database has already decided who claimed it.
func (d *Dispatcher) DispatchTo(ctx context.Context, runnerID uuid.UUID, job DispatchJob) error {
	d.mu.Lock()
	r, ok := d.runners[runnerID]
	if ok {
		r.lastDispatch = time.Now()
	}
	d.mu.Unlock()
	if !ok {
		return fmt.Errorf("runner %s is no longer connected", runnerID)
	}
	select {
	case r.ch <- toConnectResponse(job):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return fmt.Errorf("runner %s did not accept the job within 5s", runnerID)
	}
}

// toConnectResponse renders a job as the message a runner receives.
func toConnectResponse(job DispatchJob) *civ1.ConnectResponse {
	return &civ1.ConnectResponse{
		JobId:         job.JobID.String(),
		RunId:         job.RunID.String(),
		RepoCloneUrl:  job.RepoCloneURL,
		CommitSha:     job.CommitSHA,
		RunCmd:        job.RunCmd,
		AgentRole:     job.AgentRole,
		Image:         job.Image,
		Env:           job.Env,
		ArtifactPaths: job.ArtifactPaths,
	}
}
