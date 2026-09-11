package ci

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/secrets"
)

// ErrCredentialsUnavailable is returned when a job declares at least one
// secret but the broker cannot supply it. The broker fails closed: a job
// that needs credentials it cannot get must not run, but a job that needs
// none must never be blocked by the broker being down.
var ErrCredentialsUnavailable = errors.New("credentials unavailable: secret broker unreachable")

// credentialLeaseTTL is how long a job credential lease lives. It need only
// outlive one job run.
const credentialLeaseTTL = 15 * time.Minute

// ResolveJobCredentials resolves every secret job.Secrets declares into a
// redeemed value, keyed by secret name. A job declaring no secrets never
// contacts the broker at all and always succeeds with an empty, non-nil
// map — so a broker outage can never block credential-free work. A job
// declaring at least one secret that the broker cannot issue or redeem
// (including because the broker is unreachable) fails closed with
// ErrCredentialsUnavailable rather than running with partial credentials.
func ResolveJobCredentials(ctx context.Context, b secrets.BrokerClient, job Job, g capability.Grant) (map[string]string, error) {
	if len(job.Secrets) == 0 {
		return map[string]string{}, nil
	}

	runID, _ := secrets.RunIDFromContext(ctx)

	creds := make(map[string]string, len(job.Secrets))
	for _, name := range job.Secrets {
		lease, err := b.Issue(ctx, runID, g, name, credentialLeaseTTL)
		if err != nil {
			return nil, fmt.Errorf("%w: issue %q: %v", ErrCredentialsUnavailable, name, err)
		}
		value, err := b.Redeem(ctx, lease.Token)
		if err != nil {
			return nil, fmt.Errorf("%w: redeem %q: %v", ErrCredentialsUnavailable, name, err)
		}
		creds[name] = value
	}
	return creds, nil
}

// StateAfterCredentialResolution is what the scheduler should record for a
// job after attempting ResolveJobCredentials with err. A job blocked only
// because the broker is unreachable is left "pending" — it is not the job's
// fault, and it should run automatically once the broker returns — while
// any other failure is a genuine job "failure".
func StateAfterCredentialResolution(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrCredentialsUnavailable) {
		return "pending"
	}
	return "failure"
}
