package ci

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// ErrCredentialsUnavailable is returned when a job declares at least one
// secret but the broker cannot be reached. The broker fails closed: a job
// that needs credentials it cannot get must not run, but a job that needs
// none must never be blocked by the broker being down.
var ErrCredentialsUnavailable = errors.New("credentials unavailable: secret broker unreachable")

// ErrCredentialsDenied is returned when the broker answered and refused: the
// secret does not exist, is production-scoped for a staging job, or the job
// is not on the branch production material is brokered to. Waiting will not
// change that answer, so the job fails.
var ErrCredentialsDenied = errors.New("credentials denied by the secret broker")

// credentialLeaseTTL is how long a job credential lease lives. The lease is
// redeemed the moment the job is dispatched, so it need only outlive that.
const credentialLeaseTTL = 15 * time.Minute

// JobCredentials is what a job asks the broker for.
type JobCredentials struct {
	OrgID       uuid.UUID
	JobID       uuid.UUID
	RepoID      uuid.UUID
	Ref         string
	Environment string
	Secrets     []string
}

// LeaseRequest asks for one secret on behalf of one job.
type LeaseRequest struct {
	OrgID       uuid.UUID
	JobID       uuid.UUID
	RepoID      uuid.UUID
	Ref         string
	Environment string
	Name        string
	TTL         time.Duration
}

// CredentialBroker issues and redeems job credential leases. In production
// it is GatesBroker, a client of the gates service's broker; the job never
// names the grant it is issued under, the broker derives it.
type CredentialBroker interface {
	IssueJobLease(ctx context.Context, req LeaseRequest) (token string, err error)
	RedeemJobLease(ctx context.Context, orgID, jobID uuid.UUID, token string) (string, error)
}

// ResolveJobCredentials resolves every secret the job declares into its
// value, keyed by secret name. A job declaring no secrets never contacts the
// broker and always succeeds with an empty, non-nil map, so a broker outage
// can never block credential-free work. A job declaring at least one secret
// gets all of them or none: a broker it cannot reach fails with
// ErrCredentialsUnavailable, a broker that refuses fails with
// ErrCredentialsDenied, and it never runs with partial credentials.
func ResolveJobCredentials(ctx context.Context, b CredentialBroker, job JobCredentials) (map[string]string, error) {
	if len(job.Secrets) == 0 {
		return map[string]string{}, nil
	}
	if b == nil {
		return nil, fmt.Errorf("%w: this deployment has no secret broker configured", ErrCredentialsDenied)
	}
	creds := make(map[string]string, len(job.Secrets))
	for _, name := range job.Secrets {
		token, err := b.IssueJobLease(ctx, LeaseRequest{
			OrgID: job.OrgID, JobID: job.JobID, RepoID: job.RepoID, Ref: job.Ref,
			Environment: job.Environment, Name: name, TTL: credentialLeaseTTL,
		})
		if err != nil {
			return nil, classifyBrokerError("issue "+name, err)
		}
		value, err := b.RedeemJobLease(ctx, job.OrgID, job.JobID, token)
		if err != nil {
			return nil, classifyBrokerError("redeem "+name, err)
		}
		creds[name] = value
	}
	return creds, nil
}

// classifyBrokerError separates "the broker could not be asked" from "the
// broker said no". Only codes that describe a refusal count as one; anything
// else — a dead connection, a timeout, an error with no status at all — is
// treated as unreachable, which leaves the job waiting rather than failing it
// for something that was never its fault.
func classifyBrokerError(what string, err error) error {
	switch status.Code(err) {
	case codes.PermissionDenied, codes.NotFound, codes.InvalidArgument, codes.FailedPrecondition, codes.Unauthenticated:
		return fmt.Errorf("%w: %s: %s", ErrCredentialsDenied, what, status.Convert(err).Message())
	}
	return fmt.Errorf("%w: %s: %v", ErrCredentialsUnavailable, what, err)
}

// StateAfterCredentialResolution is what the dispatcher records for a job
// after attempting ResolveJobCredentials with err. A job blocked only because
// the broker is unreachable is left "pending" — it is not the job's fault,
// and it runs once the broker returns — while a refusal or any other failure
// is a genuine job "failure".
func StateAfterCredentialResolution(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrCredentialsUnavailable) {
		return "pending"
	}
	return "failure"
}

// GatesBroker is CredentialBroker over the gates service. It presents a
// service token for the job's organization — the broker brokers job
// credentials only to a platform service, never to a person or an agent.
type GatesBroker struct {
	Gates      gatesv1.GatesServiceClient
	HMACSecret string
	// Timeout bounds each call, so a broker that accepts connections and
	// never answers blocks dispatch for a bounded time, not forever.
	Timeout time.Duration
}

func (g *GatesBroker) call(ctx context.Context, orgID uuid.UUID) (context.Context, context.CancelFunc, error) {
	tok, err := svcauth.Mint(g.HMACSecret, "ci-credentials", orgID, svcauth.DefaultTTL)
	if err != nil {
		return nil, nil, err
	}
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	return metadata.AppendToOutgoingContext(ctx,
		"authorization", "Bearer "+tok, "x-novaforge-org", orgID.String()), cancel, nil
}

// IssueJobLease asks the broker for one lease.
func (g *GatesBroker) IssueJobLease(ctx context.Context, req LeaseRequest) (string, error) {
	cctx, cancel, err := g.call(ctx, req.OrgID)
	if err != nil {
		return "", err
	}
	defer cancel()
	resp, err := g.Gates.IssueJobLease(cctx, &gatesv1.IssueJobLeaseRequest{
		JobId: req.JobID.String(), RepoId: req.RepoID.String(), Ref: req.Ref,
		Environment: req.Environment, Name: req.Name, TtlSeconds: int64(req.TTL / time.Second),
	})
	if err != nil {
		return "", err
	}
	return resp.GetToken(), nil
}

// RedeemJobLease spends a lease the same job was issued.
func (g *GatesBroker) RedeemJobLease(ctx context.Context, orgID, jobID uuid.UUID, token string) (string, error) {
	cctx, cancel, err := g.call(ctx, orgID)
	if err != nil {
		return "", err
	}
	defer cancel()
	resp, err := g.Gates.RedeemLease(cctx, &gatesv1.RedeemLeaseRequest{RunId: jobID.String(), Token: token})
	if err != nil {
		return "", err
	}
	return resp.GetValue(), nil
}
