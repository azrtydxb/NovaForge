package ci_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/novaforge/novaforge/internal/ci"
)

// Hold only the call boundary; the underlying broker, Git and database are
// real. This makes the pre-dispatch interval observable without timing sleeps.
type heldBroker struct {
	ci.CredentialBroker
	entered chan struct{}
	release chan struct{}
}

func (b *heldBroker) IssueJobLease(ctx context.Context, req ci.LeaseRequest) (ci.JobCredentialLease, error) {
	select {
	case b.entered <- struct{}{}:
	case <-ctx.Done():
		return ci.JobCredentialLease{}, ctx.Err()
	}
	select {
	case <-b.release:
		return b.CredentialBroker.IssueJobLease(ctx, req)
	case <-ctx.Done():
		return ci.JobCredentialLease{}, ctx.Err()
	}
}

func TestCredentialReservationStaysPending(t *testing.T) {
	for _, name := range []string{"dispatch", "disconnect", "cancel"} {
		t.Run(name, func(t *testing.T) {
			s := newCredentialStack(t)
			ctx := context.Background()
			if err := s.putDynamicSecret("DEPLOY_TOKEN", "staging", secretValue("reservation")); err != nil {
				t.Fatal(err)
			}
			id := s.job("refs/heads/main", "wait-for-credentials", "staging", "DEPLOY_TOKEN")
			b := &heldBroker{CredentialBroker: s.pump.Credentials, entered: make(chan struct{}), release: make(chan struct{})}
			s.pump.Credentials = b
			pumpCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { defer close(done); s.pump.Run(pumpCtx) }()
			t.Cleanup(func() { cancel(); <-done })
			select {
			case <-b.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("pump never requested credentials")
			}
			j := s.jobState(id)
			if j.Status != "pending" || j.StartedAt != nil {
				t.Fatalf("job not dispatched yet: status=%s started=%v, want pending with no start", j.Status, j.StartedAt)
			}
			if _, _, err := s.store.ClaimForDispatch(ctx, s.runnerID, nil); !errors.Is(err, ci.ErrNoClaimableJob) {
				t.Fatalf("reserved job was claimable twice: %v", err)
			}
			if name == "cancel" {
				cancel()
				<-done
				if j := s.jobState(id); j.Status != "pending" || j.RunnerID != nil {
					t.Fatalf("cancellation leaked a reservation: %+v", j)
				}
				return
			}
			if name == "disconnect" {
				s.dispatcher.Unregister(ctx, s.runnerID)
				if j := s.jobState(id); j.Status != "failure" {
					t.Fatalf("disconnect left an orphan reservation: %+v", j)
				}
				if err := s.store.StartClaimedJob(ctx, id, s.runnerID); err == nil {
					t.Fatal("late credentials started a terminal job")
				}
				if err := s.store.BlockJob(ctx, id, "late broker response", time.Now()); err == nil {
					t.Fatal("late broker response resurrected a terminal job")
				}
			}
			close(b.release)
			if name == "dispatch" {
				if _, ok := s.waitDispatched(10*time.Second, 1)[id.String()]; !ok {
					t.Fatal("job was not sent after credentials resolved")
				}
				j = s.jobState(id)
				if j.Status != "running" || j.StartedAt == nil || j.Detail != "" {
					t.Fatalf("dispatch did not mark a clean running state: %+v", j)
				}
			}
		})
	}
}
