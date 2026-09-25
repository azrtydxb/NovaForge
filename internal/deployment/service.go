package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/approvals"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
)

var artifactDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type Service struct {
	pool       *pgxpool.Pool
	approvals  *approvals.Store
	targets    map[string]*Target
	resolveRun RunResolver
	lockSlots  chan struct{}
}

func NewService(pool *pgxpool.Pool, store *approvals.Store, targets []Target, resolve RunResolver) (*Service, error) {
	if pool == nil || store == nil || resolve == nil {
		return nil, errors.New("deployment requires database, approvals and live run resolver")
	}
	s := &Service{pool: pool, approvals: store, targets: make(map[string]*Target), resolveRun: resolve, lockSlots: make(chan struct{}, pool.Config().MaxConns)}
	for _, target := range targets {
		if canonical, ok := target.Executor.(interface{ Destination() string }); ok {
			if target.Destination != "" && target.Destination != canonical.Destination() {
				return nil, ErrConflict
			}
			target.Destination = canonical.Destination()
		}
		if target.Destination == "" {
			return nil, errors.New("deployment requires canonical destination")
		}
		if target.Name == "" || target.OrgID == uuid.Nil || target.RepoID == uuid.Nil || target.Revision == "" || target.Executor == nil || (target.Environment != "staging" && target.Environment != "production") {
			return nil, errors.New("invalid deployment target configuration")
		}
		if _, exists := s.targets[target.Name]; exists {
			return nil, errors.New("duplicate deployment target name")
		}
		if versioned, ok := target.Executor.(interface{ Revision() string }); ok && target.Revision != versioned.Revision() {
			return nil, errors.New("target revision does not bind executor configuration")
		}
		copy := target
		s.targets[target.Name] = &copy
	}
	return s, nil
}

func (s *Service) authorize(ctx context.Context, req Request) (authz.Scope, *Target, *time.Time, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return scope, nil, nil, err
	}
	if scope.OrgID == uuid.Nil || scope.ActorID == uuid.Nil || (scope.ActorKind != "agent" && scope.ActorKind != "user") {
		return scope, nil, nil, errors.New("deployment requires an organization actor")
	}
	if scope.ActorKind == "user" && scope.Role != "member" && !scope.IsOrgAdmin() {
		return scope, nil, nil, errors.New("deployment requires organization membership")
	}
	target, ok := s.targets[req.Target]
	if !ok || target.OrgID != scope.OrgID || target.RepoID != req.RepoID {
		return scope, nil, nil, errors.New("no deployment target in this repository and organization")
	}
	run, err := s.resolveRun(ctx, req.RunID)
	if err != nil {
		return scope, nil, nil, fmt.Errorf("resolve deployment run: %w", err)
	}
	if run.ID != req.RunID || run.OrgID != scope.OrgID || run.RepoID != req.RepoID || run.ActorID != scope.ActorID || run.ActorKind != scope.ActorKind {
		return scope, nil, nil, errors.New("deployment run does not belong to this actor and repository")
	}
	// Human membership is not a capability grant. Agents, in contrast, must
	// hold a live grant for this exact identity on every request and execution.
	grant := capability.Grant{DeployStaging: true, DeployProd: true}
	if scope.ActorKind == "agent" {
		grant = run.Grant
		if grant.OrgID != scope.OrgID || grant.SubjectID != scope.ActorID || grant.SubjectKind != scope.ActorKind || !grant.ExpiresAt.After(time.Now()) {
			return scope, nil, nil, errors.New("deployment grant missing, expired or outside actor scope")
		}
	}
	decision, err := approvals.Decide(ctx, approvals.Policy{OrgID: scope.OrgID.String()}, actionFor(target.Environment), grant)
	if err != nil {
		return scope, nil, nil, err
	}
	if decision != approvals.DecisionHuman {
		return scope, nil, nil, errors.New("deployment forbidden by policy")
	}
	ceiling := earlierExpiry(nil, run.ExpiresAt)
	if scope.ActorKind == "agent" {
		ceiling = earlierExpiry(ceiling, grant.ExpiresAt)
	}
	if ceiling != nil && !ceiling.After(time.Now()) {
		return scope, nil, nil, errors.New("deployment authority expired")
	}
	return scope, target, ceiling, nil
}

func actionFor(environment string) approvals.Action {
	if environment == "staging" {
		return approvals.ActionDeployStaging
	}
	return approvals.ActionDeployProduction
}

// Request persists intent before raising the approval. If raising fails, retry
// repairs that seam using the same id; no external action has occurred.
func (s *Service) Request(ctx context.Context, req Request) (Operation, error) {
	if req.ID == uuid.Nil || req.RunID == uuid.Nil || req.RepoID == uuid.Nil || !artifactDigest.MatchString(req.Artifact) {
		return Operation{}, errors.New("deployment requires request, run, repository ids and a sha256 artifact digest")
	}
	scope, target, ceiling, err := s.authorize(ctx, req)
	if err != nil {
		return Operation{}, err
	}
	op := Operation{AuthorizedUntil: ceiling, EffectiveUntil: ceiling, Request: req, OrgID: scope.OrgID, ActorID: scope.ActorID, ActorKind: scope.ActorKind, Environment: target.Environment, TargetRevision: target.Revision, Destination: target.Destination}
	if err = s.insert(ctx, op); err != nil {
		return Operation{}, err
	}
	stored, err := s.Get(ctx, req.ID)
	if err != nil {
		return Operation{}, err
	}
	// Replaying the same request cannot replace its original authority ceiling.
	op.AuthorizedUntil = stored.AuthorizedUntil
	if binding(stored) != binding(op) {
		return Operation{}, ErrConflict
	}
	if ceiling != nil && (stored.EffectiveUntil == nil || ceiling.Before(*stored.EffectiveUntil)) {
		// A narrowing replay and execution admission share the destination lock.
		// An active execution returns Busy; it cannot be silently rewritten.
		conn, unlock, err := s.lock(ctx, stored)
		if err != nil {
			return Operation{}, err
		}
		defer unlock()
		if _, err = conn.Exec(ctx, `UPDATE deployment.operations SET effective_until=LEAST(effective_until,$3) WHERE org_id=$1 AND id=$2`, scope.OrgID, op.ID, ceiling); err != nil {
			return Operation{}, err
		}
		stored, err = s.Get(ctx, op.ID)
		if err != nil {
			return Operation{}, err
		}
	}
	if _, err = s.approvals.EnsureDeployment(ctx, op.ID, op.RunID, actionFor(op.Environment), binding(op)); err != nil {
		return Operation{}, err
	}
	return stored, nil
}

// binding deliberately excludes execution state. Approval is for immutable
// intent, including the destination revision, never a human-readable label alone.
func binding(op Operation) string {
	// pgx may decode timestamptz in the process's local timezone. Approval
	// identity must survive a restart on a host with a different timezone.
	if op.AuthorizedUntil != nil {
		op.AuthorizedUntil = earlierExpiry(nil, *op.AuthorizedUntil)
	}
	b, _ := json.Marshal(struct {
		Request
		OrgID           uuid.UUID  `json:"org_id"`
		ActorID         uuid.UUID  `json:"actor_id"`
		ActorKind       string     `json:"actor_kind"`
		Environment     string     `json:"environment"`
		TargetRevision  string     `json:"target_revision"`
		Destination     string     `json:"destination"`
		AuthorizedUntil *time.Time `json:"authorized_until,omitempty"`
	}{op.Request, op.OrgID, op.ActorID, op.ActorKind, op.Environment, op.TargetRevision, op.Destination, op.AuthorizedUntil})
	return string(b)
}

func (s *Service) Execute(ctx context.Context, id uuid.UUID) (Operation, error) {
	return s.execute(ctx, id, false, false)
}
func (s *Service) Retry(ctx context.Context, id uuid.UUID) (Operation, error) {
	return s.execute(ctx, id, true, false)
}

func (s *Service) Reconcile(ctx context.Context, id uuid.UUID) (Operation, error) {
	return s.execute(ctx, id, false, true)
}

func (s *Service) execute(ctx context.Context, id uuid.UUID, retry, observe bool) (Operation, error) {
	op, err := s.Get(ctx, id)
	if err != nil {
		return Operation{}, err
	}
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return Operation{}, err
	}
	administrativeRecovery := observe && scope.IsOrgAdmin() && scope.ActorID != uuid.Nil
	var target *Target
	var ceiling *time.Time
	var passive PassiveObserver
	if administrativeRecovery {
		target = s.targets[op.Target]
		if target == nil || target.OrgID != scope.OrgID || target.RepoID != op.RepoID {
			return Operation{}, ErrConflict
		}
		var ok bool
		passive, ok = target.Executor.(PassiveObserver)
		if !ok {
			return Operation{}, errors.New("delivery system has no passive recovery observer")
		}
	} else {
		scope, target, ceiling, err = s.authorize(ctx, op.Request)
		if err != nil {
			return Operation{}, err
		}
		if op.ActorID != scope.ActorID || op.ActorKind != scope.ActorKind {
			return Operation{}, ErrConflict
		}
	}
	if op.TargetRevision != target.Revision || op.Environment != target.Environment || op.Destination != target.Destination {
		return Operation{}, ErrConflict
	}
	lockConn, unlock, err := s.lock(ctx, op)
	if err != nil {
		return Operation{}, err
	}
	defer unlock()
	op, err = s.Get(ctx, id)
	if err != nil {
		return Operation{}, err
	}
	approval, err := s.approvals.Get(ctx, id)
	if err != nil {
		return Operation{}, err
	}
	if approval.Decision != approvals.StateApproved || approval.DecidedBy == uuid.Nil || approval.DecidedBy == op.ActorID || approval.AuthorID != op.ActorID || approval.AuthorKind != op.ActorKind || approval.RunID != op.RunID || approval.Action != actionFor(op.Environment) || approval.Detail["raw"] != binding(op) || approval.DecidedAt.IsZero() {
		return Operation{}, ErrApprovalRequired
	}
	if !observe {
		pending, err := (&credentialJournal{conn: lockConn}).pending(ctx, op)
		if err != nil {
			return op, err
		}
		if pending {
			return op, ErrUncertain
		}
	}
	switch op.State {
	case StateSucceeded:
		return op, nil
	case StateRunning, StateUncertain:
		if !observe {
			return op, ErrUncertain
		}
	case StateFailed:
		if observe {
			return op, nil
		}
		if !retry {
			return op, ErrRetryRequired
		}
	}
	if observe && op.State == StatePending {
		return op, ErrUncertain
	}
	if !administrativeRecovery && op.ActorKind == "agent" && op.AuthorizedUntil == nil {
		return op, ErrConflict // Legacy requests lack original authority; only passive recovery is safe.
	}
	kind := "execute"
	if observe {
		kind = "observe"
	}
	if administrativeRecovery {
		kind = "recover"
	}
	op.currentCeiling = ceiling
	number, err := s.start(ctx, lockConn, op, kind)
	if err != nil {
		return Operation{}, err
	}
	op, err = s.Get(ctx, id)
	if err != nil {
		return Operation{}, err
	}
	op.journal = &credentialJournal{conn: lockConn}
	// Bound execution independently of transport timeouts; cancellation still
	// reaches the executor. Result persistence survives request cancellation.
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	op.State = StateRunning
	var result Result
	var callErr error
	if administrativeRecovery {
		result, callErr = passive.ObserveExisting(callCtx, op)
	} else if observe {
		result, callErr = target.Executor.Observe(callCtx, op)
	} else {
		result, callErr = target.Executor.Execute(callCtx, op)
	}
	cancel()
	state := StateSucceeded
	errText := ""
	if callErr != nil {
		state = StateFailed
		errText = callErr.Error()
		if (observe && !errors.Is(callErr, ErrDeliveryFailed)) || errors.Is(callErr, ErrUncertain) || errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) {
			state = StateUncertain
		}
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer finishCancel()
	pending, pendingErr := op.journal.pending(finishCtx, op)
	if pendingErr != nil || pending {
		state = StateUncertain
		callErr = fmt.Errorf("%w: credential cleanup remains pending", ErrUncertain)
		errText = callErr.Error()
	}
	if err = s.finish(finishCtx, lockConn, op, number, state, result, errText); err != nil {
		return op, fmt.Errorf("%w: persist deployment result: %v", ErrUncertain, err)
	}
	op, err = s.Get(finishCtx, id)
	if err != nil {
		return Operation{}, err
	}
	if callErr != nil {
		return op, callErr
	}
	return op, nil
}
