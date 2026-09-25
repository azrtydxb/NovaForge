// Package deployment binds a deployment action to an independently approved,
// immutable request and records each execution before calling an external system.
package deployment

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/capability"
)

var (
	ErrConflict         = errors.New("deployment request or target changed; a new request and approval are required")
	ErrApprovalRequired = errors.New("deployment requires an independent human approval")
	ErrBusy             = errors.New("another deployment is executing on this target")
	ErrRetryRequired    = errors.New("deployment failed; use explicit retry")
	ErrDeliveryFailed   = errors.New("verified delivery failure")
	ErrUncertain        = errors.New("deployment outcome is uncertain; reconcile the external operation before retry")
)

const (
	StatePending   = "pending"
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateUncertain = "uncertain"
)

// Target comes from trusted service configuration, never repository/model input.
// Revision must change whenever the external destination or execution policy
// changes. The executor owns a single fixed destination, not caller-supplied URLs,
// commands, namespaces or manifests. Registering a target does not approve it.
type Target struct {
	// Destination is an operator-owned canonical identity, stable across revisions.
	Destination string
	Name        string
	OrgID       uuid.UUID
	RepoID      uuid.UUID
	Environment string
	Revision    string
	Executor    Executor
}

// Executor must reconcile the stable Operation.ID before repeating an external
// action. A normal error means execution definitively failed; an ambiguous
// transport error MUST wrap ErrUncertain. Success means the desired deployment
// was observed, not merely that the remote system accepted a request.
type Executor interface {
	Execute(context.Context, Operation) (Result, error)
	// Observe errors remain uncertain unless they wrap ErrDeliveryFailed, which
	// requires verified delivery evidence, not merely a failed observation.
	// Observe is read-only against the delivery system; it resolves an unknown
	// attempt without resubmitting an upgrade.
	Observe(context.Context, Operation) (Result, error)
}

// PassiveObserver reads already-existing evidence without creating a workload,
// issuing credentials, or mutating the delivery target. Administrators may use
// this recovery path after an author grant expires.
type PassiveObserver interface {
	ObserveExisting(context.Context, Operation) (Result, error)
}

type Result struct {
	ExternalID string `json:"external_id"`
	Summary    string `json:"summary"`
}

// Run is resolved afresh through the owning service, including the live grant.
// Neither grant bits nor a run's ownership may come from a deployment request.
type Run struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	RepoID    uuid.UUID
	ActorID   uuid.UUID
	ActorKind string
	ExpiresAt time.Time // Fixed run-owner wallclock ceiling; zero only when the owner has none.
	Grant     capability.Grant
}
type RunResolver func(context.Context, uuid.UUID) (Run, error)

type Request struct {
	ID     uuid.UUID `json:"id"`
	RunID  uuid.UUID `json:"run_id"`
	RepoID uuid.UUID `json:"repo_id"`
	Target string    `json:"target"`
	// Artifact is an immutable content digest, not a tag or executable argument.
	Artifact string `json:"artifact"`
}

type Operation struct {
	Request
	OrgID          uuid.UUID              `json:"org_id"`
	ActorID        uuid.UUID              `json:"actor_id"`
	ActorKind      string                 `json:"actor_kind"`
	Environment    string                 `json:"environment"`
	TargetRevision string                 `json:"target_revision"`
	Destination    string                 `json:"destination"`
	State          string                 `json:"state"`
	CreatedAt      time.Time              `json:"created_at"`
	Attempts       []Attempt              `json:"attempts"`
	Credentials    []CredentialObligation `json:"credentials"`
	// AuthorizedUntil is fixed at request creation, never renewed by approval or retry.
	AuthorizedUntil *time.Time `json:"authorized_until,omitempty"`
	// EffectiveUntil may only narrow as matching requests and attempts revalidate.
	// It is not approval identity and never rewrites an issued attempt's request.
	EffectiveUntil *time.Time `json:"effective_until,omitempty"`
	currentCeiling *time.Time
	journal        *credentialJournal
}

type Attempt struct {
	AuthorizedUntil     *time.Time `json:"authorized_until,omitempty"`
	CredentialExpiresAt *time.Time `json:"credential_expires_at,omitempty"`
	ActorID             uuid.UUID  `json:"actor_id"`
	ActorKind           string     `json:"actor_kind"`
	Kind                string     `json:"kind"`
	Number              int        `json:"number"`
	State               string     `json:"state"`
	StartedAt           time.Time  `json:"started_at"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
	Result              Result     `json:"result"`
	Error               string     `json:"error,omitempty"`
}

// CredentialObligation survives request/process loss independently of results.
// ResolvedAt means the configured provider confirmed revocation AND fenced late
// issuance for this exact attempt; neither TTL nor a missing Secret proves that.
type CredentialObligation struct {
	Attempt         int        `json:"attempt"`
	ProviderBinding string     `json:"provider_binding"`
	Phase           string     `json:"phase"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
}
