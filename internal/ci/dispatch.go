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
	ConnectionID uuid.UUID
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
	// Secrets and Environment are what the job declared; RepoID and Ref are
	// what the broker judges a production request by.
	Secrets     []string
	Environment string
	RepoID      uuid.UUID
	Ref         string
	// SecretEnv is the brokered credentials, set by the pump just before the
	// job is sent and never stored.
	SecretEnv map[string]string
}

// registeredRunner is one runner currently holding an open Connect stream.
type registeredRunner struct {
	connectionID uuid.UUID
	ch           chan<- *civ1.ConnectResponse
	labels       map[string]struct{}
	lastDispatch time.Time
}

// Dispatcher tracks every runner's outbound stream channel and picks a
// matching, least-recently-used runner for each job. Runners hold a
// persistent outbound gRPC stream and the platform pushes jobs down it, so
// a runner never needs inbound network reachability.
type Dispatcher struct {
	// Serializes this replica's durable replacement with local activation.
	// An older labels lookup must not register after its replacement.
	activationMu sync.Mutex
	mu           sync.Mutex
	runners      map[uuid.UUID]*registeredRunner
	store        *Store
}

// NewDispatcher builds a Dispatcher whose reaper marks jobs orphaned by a
// disconnected runner as failed through store. store may be nil in tests
// that only exercise in-memory dispatch and never call Unregister.
func NewDispatcher(store *Store) *Dispatcher {
	return &Dispatcher{runners: make(map[uuid.UUID]*registeredRunner), store: store}
}

// Register records runnerID as connected and able to receive jobs whose
// required labels are a subset of labels, delivered on ch.
func (d *Dispatcher) Register(runnerID uuid.UUID, labels []string, ch chan<- *civ1.ConnectResponse, connections ...uuid.UUID) {
	set := make(map[string]struct{}, len(labels))
	for _, l := range labels {
		set[l] = struct{}{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var connection uuid.UUID
	if len(connections) > 0 {
		connection = connections[0]
	}
	d.runners[runnerID] = &registeredRunner{ch: ch, labels: set, connectionID: connection}
}

// Unregister removes runnerID from the dispatch pool and reaps any job still
// reserved or running against it: a runner that disconnects mid-job — dies, loses
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
	jobIDs, err := d.store.ActiveJobsForRunner(ctx, runnerID)
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
	ConnectionID uuid.UUID
	ID           uuid.UUID
	Labels       []string
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
		out = append(out, ConnectedRunner{ID: id, Labels: labels, ConnectionID: r.connectionID})
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
	if !ok || r.connectionID != job.ConnectionID {
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
		ConnectionId:  job.ConnectionID.String(),
		RunId:         job.RunID.String(),
		RepoCloneUrl:  job.RepoCloneURL,
		CommitSha:     job.CommitSHA,
		RunCmd:        job.RunCmd,
		AgentRole:     job.AgentRole,
		Image:         job.Image,
		Env:           job.Env,
		ArtifactPaths: job.ArtifactPaths,
		SecretEnv:     job.SecretEnv,
	}
}

// UnregisterConnection never lets an old stream remove its replacement.
func (d *Dispatcher) UnregisterConnection(ctx context.Context, runner, connection uuid.UUID) {
	d.mu.Lock()
	if current := d.runners[runner]; current != nil && current.connectionID == connection {
		delete(d.runners, runner)
	}
	d.mu.Unlock()
	if d.store != nil {
		_ = d.store.EndRunnerConnection(ctx, runner, connection)
	}
}

// activateConnection is the sole production handshake activation path. The
// hello is queued while holding the map lock, before any pump can queue a job.
func (d *Dispatcher) activateConnection(ctx context.Context, runner uuid.UUID, ch chan<- *civ1.ConnectResponse) (uuid.UUID, error) {
	d.activationMu.Lock()
	defer d.activationMu.Unlock()
	connection, err := d.store.BeginRunnerConnection(ctx, runner)
	if err != nil {
		return connection, err
	}
	labels, err := d.store.RunnerLabels(ctx, runner)
	if err != nil {
		return connection, err
	}
	set := map[string]struct{}{}
	for _, label := range labels {
		set[label] = struct{}{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	select {
	case ch <- &civ1.ConnectResponse{ConnectionId: connection.String()}:
	case <-ctx.Done():
		return connection, ctx.Err()
	}
	d.runners[runner] = &registeredRunner{connectionID: connection, ch: ch, labels: set}
	return connection, nil
}
