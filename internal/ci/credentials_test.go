package ci_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/novaforge/novaforge/internal/ci"
)

// downBroker answers every call the way a gRPC client to an unreachable
// broker does. The deployed path is exercised by TestBrokerDownFailsClosed
// against a real stopped server; this pins the classification alone.
type downBroker struct{ code codes.Code }

func (d downBroker) IssueJobLease(context.Context, ci.LeaseRequest) (ci.JobCredentialLease, error) {
	return ci.JobCredentialLease{}, status.Error(d.code, "secret broker says no")
}

func (d downBroker) RedeemJobLease(context.Context, uuid.UUID, uuid.UUID, string) (string, error) {
	return "", status.Error(d.code, "secret broker says no")
}

func TestCredentialFreeJobRunsWhenBrokerDown(t *testing.T) {
	creds, err := ci.ResolveJobCredentials(context.Background(), downBroker{codes.Unavailable}, ci.JobCredentials{})
	if err != nil {
		t.Fatalf("want nil error for a credential-free job even with the broker down, got %v", err)
	}
	if len(creds) != 0 {
		t.Fatalf("want an empty credential map, got %v", creds)
	}
}

func TestCredentialJobBlocksWhenBrokerDown(t *testing.T) {
	job := ci.JobCredentials{Secrets: []string{"DEPLOY_KEY"}}
	_, err := ci.ResolveJobCredentials(context.Background(), downBroker{codes.Unavailable}, job)
	if !errors.Is(err, ci.ErrCredentialsUnavailable) {
		t.Fatalf("want ErrCredentialsUnavailable, got %v", err)
	}
}

// TestJobStaysQueuedNotFailed pins the split between a broker that could not
// be asked (the job waits) and one that refused (the job fails): treating a
// refusal as an outage would retry a production secret for a staging job
// forever, and treating an outage as a refusal would fail good jobs.
func TestJobStaysQueuedNotFailed(t *testing.T) {
	job := ci.JobCredentials{Secrets: []string{"DEPLOY_KEY"}}

	_, err := ci.ResolveJobCredentials(context.Background(), downBroker{codes.Unavailable}, job)
	if state := ci.StateAfterCredentialResolution(err); state != "pending" {
		t.Fatalf("want a broker-down job to stay 'pending', got %q", state)
	}

	_, err = ci.ResolveJobCredentials(context.Background(), downBroker{codes.PermissionDenied}, job)
	if !errors.Is(err, ci.ErrCredentialsDenied) {
		t.Fatalf("want a refusal to be ErrCredentialsDenied, got %v", err)
	}
	if state := ci.StateAfterCredentialResolution(err); state != "failure" {
		t.Fatalf("want a refused job to report 'failure', got %q", state)
	}

	_, err = ci.ResolveJobCredentials(context.Background(), nil, job)
	if state := ci.StateAfterCredentialResolution(err); state != "failure" {
		t.Fatalf("want a job needing a secret on a deployment with no broker to fail, got %q (%v)", state, err)
	}
}

func (d downBroker) RevokeJobLeases(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (ci.CredentialCleanup, error) {
	return ci.CredentialCleanup{}, status.Error(d.code, "broker unavailable")
}
