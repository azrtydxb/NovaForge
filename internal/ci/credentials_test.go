package ci_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/ci"
	"github.com/novaforge/novaforge/internal/secrets"
)

// downBroker simulates a secret broker that cannot be reached: every call
// fails with a gRPC Unavailable error.
type downBroker struct{}

func (downBroker) Issue(context.Context, uuid.UUID, capability.Grant, string, time.Duration) (secrets.Lease, error) {
	return secrets.Lease{}, status.Error(codes.Unavailable, "secret broker unreachable")
}

func (downBroker) Redeem(context.Context, string) (string, error) {
	return "", status.Error(codes.Unavailable, "secret broker unreachable")
}

func TestCredentialFreeJobRunsWhenBrokerDown(t *testing.T) {
	job := ci.Job{Run: "echo hi"} // declares no secrets

	creds, err := ci.ResolveJobCredentials(context.Background(), downBroker{}, job, capability.Grant{})
	if err != nil {
		t.Fatalf("want nil error for a credential-free job even with the broker down, got %v", err)
	}
	if len(creds) != 0 {
		t.Fatalf("want an empty credential map, got %v", creds)
	}
}

func TestCredentialJobBlocksWhenBrokerDown(t *testing.T) {
	job := ci.Job{Run: "deploy.sh", Secrets: []string{"DEPLOY_KEY"}}

	_, err := ci.ResolveJobCredentials(context.Background(), downBroker{}, job, capability.Grant{})
	if err == nil {
		t.Fatal("want an error when a credential-needing job's broker is unreachable")
	}
	if !errors.Is(err, ci.ErrCredentialsUnavailable) {
		t.Fatalf("want error satisfying errors.Is(err, ci.ErrCredentialsUnavailable), got %v", err)
	}
}

func TestJobStaysQueuedNotFailed(t *testing.T) {
	job := ci.Job{Run: "deploy.sh", Secrets: []string{"DEPLOY_KEY"}}

	_, err := ci.ResolveJobCredentials(context.Background(), downBroker{}, job, capability.Grant{})
	if err == nil {
		t.Fatal("want an error to determine job state from")
	}

	state := ci.StateAfterCredentialResolution(err)
	if state != "pending" {
		t.Fatalf("want a broker-down job to stay 'pending', got %q", state)
	}

	// A genuine (non-broker) failure is reported as failure, not pending.
	otherState := ci.StateAfterCredentialResolution(errors.New("some other error"))
	if otherState != "failure" {
		t.Fatalf("want a non-broker error to report 'failure', got %q", otherState)
	}
}
