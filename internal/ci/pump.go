package ci

import (
	"context"
	"errors"
	"fmt"
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

// tick offers work to each connected runner once.
func (p *Pump) tick(ctx context.Context) error {
	for _, r := range p.dispatcher.Connected() {
		job, orgID, err := p.store.ClaimForDispatch(ctx, r.ID, r.Labels)
		if errors.Is(err, ErrNoClaimableJob) {
			// Nothing to do for this runner is the normal case, not an error.
			continue
		}
		if err != nil {
			// Anything else is a real failure and must be visible: swallowing it
			// as "no work" is indistinguishable from an idle system, which is
			// how a broken claim path stays hidden.
			if ctx.Err() == nil {
				p.log.Error("ci pump: claim job", "runner", r.ID, "error", err)
			}
			continue
		}
		// ClaimForDispatch hands back the repository NAME here; the pump turns
		// it into a credentialed URL.
		job.RepoCloneURL, err = p.cloneURL(orgID, job.RepoCloneURL)
		if err != nil {
			p.log.Error("ci pump: build clone url", "job", job.JobID, "error", err)
			_ = p.store.SetJobStatus(ctx, job.JobID, "failure", err.Error())
			continue
		}

		if err := p.dispatcher.DispatchTo(ctx, r.ID, job); err != nil {
			// The runner vanished between the claim and the send; put the job
			// back so another runner takes it rather than losing it.
			p.log.Warn("ci pump: dispatch failed, releasing job", "job", job.JobID, "error", err)
			_ = p.store.SetJobStatus(ctx, job.JobID, "pending", "")
		}
	}
	return nil
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
