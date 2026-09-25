package ci

import (
	"context"
	"errors"
	"fmt"
	"github.com/novaforge/novaforge/internal/redact"
	"github.com/novaforge/novaforge/internal/svcauth"
	"log/slog"
	"net/url"
	"time"

	"github.com/google/uuid"
)

// pumpInterval is how often the pump looks for claimable work. It is short
// because the latency a person notices after a push is mostly this.
const pumpInterval = 2 * time.Second

// Pump moves pending jobs to connected runners.
//
// Without it the two halves of CI never met: the scheduler created jobs and
// runners held open streams, but nothing claimed a job on a runner's behalf and
// pushed it down, so every run sat queued forever with no error anywhere.
//
// Claiming is per-runner and uses SELECT ... FOR UPDATE SKIP LOCKED inside the
// store, so two pumps — or two replicas of this service — cannot hand the same
// job to two runners.
type Pump struct {
	store      *Store
	dispatcher *Dispatcher
	cloneBase  string
	hmacSecret string
	log        *slog.Logger
	interval   time.Duration

	// Credentials brokers the secrets a job declares, at dispatch. Nil means
	// the deployment has no broker: a job declaring a secret then fails
	// saying so, rather than running without it.
	Credentials CredentialBroker
	// Redactions receives each dispatched job's credential values, so the
	// log path can mask them.
	Redactions *Redactions
}

// NewPump builds a Pump. cloneBase is the base URL a runner clones from, e.g.
// "http://novaforge-git-platform:8081".
func NewPump(store *Store, dispatcher *Dispatcher, cloneBase, hmacSecret string) *Pump {
	return &Pump{
		store:      store,
		dispatcher: dispatcher,
		cloneBase:  cloneBase,
		hmacSecret: hmacSecret,
		log:        slog.Default(),
		interval:   pumpInterval,
	}
}

// Run pumps until ctx is cancelled.
func (p *Pump) Run(ctx context.Context) {
	go p.runCredentialCleanup(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.tick(ctx); err != nil && ctx.Err() == nil {
				p.log.Error("ci pump", "error", err)
			}
		}
	}
}

// maxClaimsPerRunner bounds how many jobs one tick claims for one runner
// while looking for one it can send. A job whose credentials cannot be
// brokered is set aside and the next is tried, so it does not starve the jobs
// behind it; the bound keeps a queue of such jobs from spinning a tick.
const maxClaimsPerRunner = 8

// credentialRetryAfter is how long a job blocked on an unreachable broker
// waits before it is claimed again.
const credentialRetryAfter = 15 * time.Second

// tick offers work to each connected runner: at most one dispatched job per
// runner per tick.
func (p *Pump) tick(ctx context.Context) error {
	for _, r := range p.dispatcher.Connected() {
		for i := 0; i < maxClaimsPerRunner; i++ {
			if p.offer(ctx, r) {
				break
			}
		}
	}
	return nil
}

// offer claims one job for r and tries to send it. It reports whether the
// runner is done for this tick — a job was sent, or there is nothing to claim
// — rather than whether a job was set aside and another should be tried.
func (p *Pump) offer(ctx context.Context, r ConnectedRunner) bool {
	job, orgID, err := p.store.ClaimForDispatch(ctx, r.ID, r.Labels, r.ConnectionID)
	if errors.Is(err, ErrNoClaimableJob) {
		// Nothing to do for this runner is the normal case, not an error.
		return true
	}
	if err != nil {
		// Anything else is a real failure and must be visible: swallowing it
		// as "no work" is indistinguishable from an idle system, which is
		// how a broken claim path stays hidden.
		if ctx.Err() == nil {
			p.log.Error("ci pump: claim job", "runner", r.ID, "error", err)
		}
		return true
	}
	// ClaimForDispatch hands back the repository NAME here; the pump turns
	// it into a credentialed URL.
	job.RepoCloneURL, err = p.cloneURL(orgID, job.RepoCloneURL)
	if err != nil {
		p.log.Error("ci pump: build clone url", "job", job.JobID, "error", err)
		_ = p.store.SetJobStatus(ctx, job.JobID, "failure", err.Error())
		return false
	}

	// Credentials are brokered here, at the last moment before the job is
	// sent, and never stored: the lease is issued and redeemed for this job
	// only, and the value lives in memory until the runner has it. A job that
	// cannot get them does not run — it waits if the broker is unreachable
	// and fails if the broker refused.
	attempt := uuid.Nil
	abandon := func(cause error) bool {
		if attempt == uuid.Nil {
			return true
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var ids []uuid.UUID
		var ce *CredentialCleanupError
		if errors.As(cause, &ce) {
			ids = ce.LeaseIDs
		}
		if err := p.store.abandonCredentials(cleanup, orgID, job.JobID, attempt, ids); err != nil {
			p.log.Error("ci credential cleanup not persisted; reservation retained", "job", job.JobID, "error", err)
			return false
		}
		return true
	}
	if len(job.Secrets) > 0 {
		attempt = uuid.New()
		if err := p.store.prepareCredentials(ctx, orgID, job.JobID, r.ID, attempt); err != nil {
			p.releaseClaim(ctx, job.JobID, "credential preparation unavailable", time.Now().Add(credentialRetryAfter))
			return false
		}
		creds, cerr := ResolveJobCredentials(ctx, p.Credentials, JobCredentials{
			AttemptID: attempt, OrgID: orgID, JobID: job.JobID, RepoID: job.RepoID, Ref: job.Ref,
			Environment: job.Environment, Secrets: job.Secrets,
		})
		if cerr != nil {
			if !abandon(cerr) {
				return true
			}
			if StateAfterCredentialResolution(cerr) == "pending" {
				p.log.Warn("ci pump: job blocked on credentials", "job", job.JobID, "error", cerr)
				p.releaseClaim(ctx, job.JobID, "blocked: "+cerr.Error(), time.Now().Add(credentialRetryAfter))
			} else {
				p.log.Warn("ci pump: job credentials refused", "job", job.JobID, "error", cerr)
				_ = p.store.SetJobStatus(ctx, job.JobID, "failure", cerr.Error())
			}
			return false
		}
		job.SecretEnv = creds
		p.Redactions.Register(job.JobID, redact.Values(creds))
	}

	if err := p.store.StartClaimedJob(ctx, job.JobID, r.ID, attempt); err != nil {
		p.log.Warn("ci pump: cannot start reserved job", "job", job.JobID, "error", err)
		p.Redactions.Forget(job.JobID)
		if !abandon(nil) {
			return true
		}
		p.releaseClaim(ctx, job.JobID, "", time.Now())
		return true
	}
	if err := p.dispatcher.DispatchTo(ctx, r.ID, job); err != nil {
		// The runner vanished between the claim and the send; put the job
		// back so another runner takes it rather than losing it. Its lease
		// is spent, so it is brokered afresh when it is claimed again.
		p.log.Warn("ci pump: dispatch failed, releasing job", "job", job.JobID, "error", err)
		p.Redactions.Forget(job.JobID)
		if !abandon(nil) {
			return true
		}
		p.releaseClaim(ctx, job.JobID, "", time.Now())
	}
	return true
}

// releaseClaim must still release the reservation when the pump is cancelled.
// BlockJob refuses terminal jobs, so disconnect cleanup cannot be undone.
func (p *Pump) releaseClaim(ctx context.Context, jobID uuid.UUID, detail string, until time.Time) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := p.store.BlockJob(cleanup, jobID, detail, until); err != nil {
		p.log.Warn("ci pump: reservation not released", "job", jobID, "error", err)
	}
}

// cloneURL addresses the repository by id — git-platform resolves that the same
// way it resolves a name — and carries a short-lived service token scoped to the
// job's organization.
//
// A CI job has no person behind it, so it cannot borrow anyone's credential;
// without one the clone simply fails with git's generic exit 128 and no
// indication that authentication was the problem.
func (p *Pump) cloneURL(orgID uuid.UUID, repoName string) (string, error) {
	if p.cloneBase == "" {
		return "", nil
	}
	tok, err := svcauth.Mint(p.hmacSecret, "ci-pump", orgID, jobCloneTokenTTL)
	if err != nil {
		return "", fmt.Errorf("mint clone token: %w", err)
	}
	u, err := url.Parse(p.cloneBase)
	if err != nil {
		return "", fmt.Errorf("parse clone base %q: %w", p.cloneBase, err)
	}
	u.User = url.UserPassword("ci", tok)
	if repoName == "" {
		return "", fmt.Errorf("run carries no repository name, so a clone url cannot be built")
	}
	u.Path = fmt.Sprintf("/%s/%s.git", orgID, repoName)
	return u.String(), nil
}

// jobCloneTokenTTL bounds the credential a job clones with. It outlives a
// normal checkout and little else.
const jobCloneTokenTTL = 30 * time.Minute
