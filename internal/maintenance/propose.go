package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/work"
)

// proposeTickInterval is how often Proposer.Run rescans every repository.
const proposeTickInterval = 24 * time.Hour

// Proposer turns maintenance Findings into Work Items for approval. It
// never executes a fix and never starts an Agent Run — Propose's only
// effect is creating or closing a plain, unassigned Work Item; approving
// and executing the fix are entirely someone else's decision, later.
type Proposer struct {
	Work *work.Store
}

// Fingerprint returns a stable identifier for a Finding — its Kind, Title,
// and sorted Paths — so the identical finding proposed on two different
// scans resolves to the same Work Item instead of a duplicate.
func Fingerprint(f Finding) string {
	paths := append([]string(nil), f.Paths...)
	sort.Strings(paths)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s", f.Kind, f.Title, strings.Join(paths, "\x00"))
	return hex.EncodeToString(h.Sum(nil))
}

// findingType maps a Finding onto the work.Item type Propose creates it
// as: the finding's own ProposedType, when set, and "tech_debt" as a
// generic fallback otherwise.
func findingType(f Finding) string {
	if f.ProposedType != "" {
		return f.ProposedType
	}
	return "tech_debt"
}

// Propose creates one Work Item per finding in findings not already
// proposed for (orgID, repoID) — deduplicated by Fingerprint — and closes
// (moves to state "done", with a note) any previously proposed Work Item
// for (orgID, repoID) whose fingerprint is absent from findings, since
// that finding no longer reproduces on this scan.
//
// Every created Work Item is state "open" with NO assignee — see
// work.Store.ProposeFinding — and Propose calls nothing in the agents
// package at all: there is no code path here that could start an Agent
// Run, which is what makes "propose for approval, never execute
// unapproved" structural rather than a convention this file could
// accidentally violate.
func (p *Proposer) Propose(ctx context.Context, orgID, repoID uuid.UUID, findings []Finding) ([]work.Item, error) {
	ctx = authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorKind: "system"})

	current := make(map[string]Finding, len(findings))
	for _, f := range findings {
		current[Fingerprint(f)] = f
	}

	items := make([]work.Item, 0, len(current))
	for fp, f := range current {
		item, err := p.Work.ProposeFinding(ctx, orgID, repoID, fp, work.Item{
			Type: findingType(f),
			Goal: proposalGoal(f),
		})
		if err != nil {
			return nil, fmt.Errorf("maintenance: propose finding %q: %w", f.Title, err)
		}
		items = append(items, item)
	}

	open, err := p.Work.OpenProposalFingerprints(ctx, orgID, repoID)
	if err != nil {
		return nil, fmt.Errorf("maintenance: propose: list open proposals: %w", err)
	}
	for fp := range open {
		if _, stillFound := current[fp]; stillFound {
			continue
		}
		if err := p.Work.ResolveProposal(ctx, orgID, repoID, fp, "no longer detected by a subsequent maintenance scan"); err != nil {
			return nil, fmt.Errorf("maintenance: propose: resolve stale proposal: %w", err)
		}
	}

	return items, nil
}

// proposalGoal renders a Finding's title and detail into the single Goal
// string work.Item carries.
func proposalGoal(f Finding) string {
	if f.Detail == "" {
		return f.Title
	}
	return fmt.Sprintf("%s\n\n%s", f.Title, f.Detail)
}

// RepoRef names one repository within one organization.
type RepoRef struct {
	OrgID  uuid.UUID
	RepoID uuid.UUID
}

// RepoLister names every repository Proposer.Run should scan.
type RepoLister func(ctx context.Context) ([]RepoRef, error)

// ScanRepo produces the findings for one repository. Production wiring
// backs this with RunAll plus whatever raw material (a checkout, job
// history, graph access) it needs to assemble for that repository; tests
// supply a stub.
type ScanRepo func(ctx context.Context, ref RepoRef) ([]Finding, error)

// Run scans every repository RepoLister names on a 24h ticker, proposing
// Work Items for whatever each scan finds, until ctx is cancelled. One
// repository's scan or proposal error is reported through onError (if
// non-nil) and does not stop the rest — the same "isolate one failure"
// principle the scanners themselves use.
func (p *Proposer) Run(ctx context.Context, repos RepoLister, scan ScanRepo, onError func(ref RepoRef, err error)) error {
	if repos == nil || scan == nil {
		return fmt.Errorf("maintenance: Proposer.Run requires a RepoLister and a ScanRepo")
	}
	ticker := time.NewTicker(proposeTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			list, err := repos(ctx)
			if err != nil {
				if onError != nil {
					onError(RepoRef{}, err)
				}
				continue
			}
			for _, ref := range list {
				findings, err := scan(ctx, ref)
				if err != nil {
					if onError != nil {
						onError(ref, err)
					}
					continue
				}
				if _, err := p.Propose(ctx, ref.OrgID, ref.RepoID, findings); err != nil && onError != nil {
					onError(ref, err)
				}
			}
		}
	}
}
