package maintenance

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/analysis"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/novaforge/novaforge/internal/work"
)

// SweepService is the name the sweep's service tokens carry.
const SweepService = "maintenance-sweep"

// OrgLister names the organizations a sweep covers. It returns ids only.
type OrgLister func(ctx context.Context) ([]uuid.UUID, error)

// Sweeper is the production path from a repository to maintenance proposals,
// shared by the periodic sweep and by a person's scan request.
//
// It lived in cmd/work-reviews, where nothing could test it: the scanners and
// the proposer were each proven, and the code joining them — reading the
// repository through the git service, running the real tools, proposing — was
// exercised by no test at all.
type Sweeper struct {
	Proposer   *Proposer
	Git        gitv1.GitServiceClient
	HMACSecret string
	// Orgs names the organizations a sweep covers. Production uses
	// GitOrganizations: every organization that has a repository.
	Orgs OrgLister
	// Exec runs the analysis tools; nil means analysis.DefaultExec.
	Exec analysis.Exec
}

// NewSweeper is the sweep work-reviews runs.
func NewSweeper(store *work.Store, git gitv1.GitServiceClient, hmacSecret string) *Sweeper {
	return &Sweeper{
		Proposer:   &Proposer{Work: store},
		Git:        git,
		HMACSecret: hmacSecret,
		Orgs:       GitOrganizations(git, hmacSecret),
	}
}

// GitOrganizations lists every organization with at least one repository by
// asking git-platform, the service that owns repositories, with a platform
// token. The sweep once listed organizations through the work schema, which
// was the only list it could read without crossing into another service's
// schema — so an organization with repositories and no Work Item was never
// swept, and a first CVE could never propose the first Work Item.
func GitOrganizations(git gitv1.GitServiceClient, hmacSecret string) OrgLister {
	return func(ctx context.Context) ([]uuid.UUID, error) {
		tok, err := svcauth.MintPlatform(hmacSecret, SweepService, svcauth.DefaultTTL)
		if err != nil {
			return nil, fmt.Errorf("mint platform token: %w", err)
		}
		callCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
		resp, err := git.ListOrganizationsWithRepositories(callCtx, &gitv1.ListOrganizationsWithRepositoriesRequest{})
		if err != nil {
			return nil, fmt.Errorf("list organizations with repositories: %w", err)
		}
		out := make([]uuid.UUID, 0, len(resp.GetOrgIds()))
		for _, raw := range resp.GetOrgIds() {
			id, err := uuid.Parse(raw)
			if err != nil {
				return nil, fmt.Errorf("git-platform returned an invalid organization id %q", raw)
			}
			out = append(out, id)
		}
		return out, nil
	}
}

// Run sweeps after firstDelay and then every interval until ctx is cancelled.
// The first sweep does not wait a whole interval: every deploy restarts the
// process, and on a cluster deployed more often than the interval a ticker
// alone never fired, so nothing was ever proposed.
func (s *Sweeper) Run(ctx context.Context, firstDelay, every time.Duration) {
	first := time.NewTimer(firstDelay)
	defer first.Stop()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			s.Sweep(ctx)
		case <-ticker.C:
			s.Sweep(ctx)
		}
	}
}

// SweepReport says what one sweep did.
type SweepReport struct {
	Organizations int
	Repositories  int
	Proposed      []string
	Errors        []string
}

// Sweep scans every repository of every organization Orgs names, once. One
// organization's or repository's failure is recorded and the rest carry on.
func (s *Sweeper) Sweep(ctx context.Context) SweepReport {
	var rep SweepReport
	if s.Orgs == nil {
		rep.Errors = append(rep.Errors, "no organization lister configured")
		return rep
	}
	orgs, err := s.Orgs(ctx)
	if err != nil {
		log.Printf("maintenance: list organizations: %v", err)
		rep.Errors = append(rep.Errors, err.Error())
		return rep
	}
	rep.Organizations = len(orgs)
	for _, orgID := range orgs {
		// The worker re-enters each organization's scope with a token naming
		// only that organization, so nothing it reads can come from another.
		callCtx, err := s.orgContext(ctx, orgID)
		if err != nil {
			rep.Errors = append(rep.Errors, err.Error())
			continue
		}
		repos, err := s.Git.ListRepos(callCtx, &gitv1.ListReposRequest{})
		if err != nil {
			log.Printf("maintenance: list repos for org %s: %v", orgID, err)
			rep.Errors = append(rep.Errors, fmt.Sprintf("org %s: %v", orgID, err))
			continue
		}
		for _, r := range repos.GetRepos() {
			repoID, perr := uuid.Parse(r.GetId())
			if perr != nil {
				continue
			}
			rep.Repositories++
			res, serr := s.ScanAndPropose(callCtx, orgID, repoID, r.GetName(), r.GetDefaultBranch())
			if serr != nil {
				log.Printf("maintenance: scan %s: %v", r.GetName(), serr)
				rep.Errors = append(rep.Errors, fmt.Sprintf("%s: %v", r.GetName(), serr))
				continue
			}
			rep.Proposed = append(rep.Proposed, res.ProposedKeys...)
		}
	}
	return rep
}

func (s *Sweeper) orgContext(ctx context.Context, orgID uuid.UUID) (context.Context, error) {
	tok, err := svcauth.Mint(s.HMACSecret, SweepService, orgID, svcauth.DefaultTTL)
	if err != nil {
		return nil, fmt.Errorf("mint service token for org %s: %w", orgID, err)
	}
	ctx = authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorKind: "service"})
	return metadata.AppendToOutgoingContext(ctx,
		"authorization", "Bearer "+tok,
		"x-novaforge-org", orgID.String(),
	), nil
}

// Scanner returns the on-demand scan a person requests. It reads the
// repository as the caller: their credential, forwarded on the git
// connection, is what reads it.
func (s *Sweeper) Scanner() work.Scanner {
	return func(ctx context.Context, orgID, repoID uuid.UUID) (work.ScanResult, error) {
		repo, err := s.Git.GetRepo(ctx, &gitv1.GetRepoRequest{Name: repoID.String()})
		if err != nil {
			return work.ScanResult{}, fmt.Errorf("resolve repository: %w", err)
		}
		return s.ScanAndPropose(ctx, orgID, repoID, repo.GetRepo().GetName(), repo.GetRepo().GetDefaultBranch())
	}
}

// ScanAndPropose checks the repository's default branch out through the git
// service, runs every scanner against it, and proposes a Work Item per
// finding. Nothing executes a fix and nothing starts an agent.
func (s *Sweeper) ScanAndPropose(ctx context.Context, orgID, repoID uuid.UUID, name, defaultBranch string) (work.ScanResult, error) {
	var res work.ScanResult
	dir, err := os.MkdirTemp("", "novaforge-maintenance-*")
	if err != nil {
		return res, fmt.Errorf("create scan directory: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := materialiseDir(ctx, s.Git, repoID, defaultBranch, "", dir, 0); err != nil {
		return res, err
	}

	exec := s.Exec
	if exec == nil {
		exec = analysis.DefaultExec
	}
	in := ScanInput{
		OrgID:     orgID,
		RepoID:    repoID,
		WorkDir:   dir,
		TargetRef: defaultBranch,
		Exec:      exec,
		Git:       s.Git,
	}
	findings := RunAll(ctx, in, func(kind string, err error) {
		log.Printf("maintenance: %s scanner on %s: %v", kind, name, err)
		res.ScannerErrors = append(res.ScannerErrors, kind+": "+err.Error())
	})
	res.Findings = len(findings)
	if len(findings) == 0 {
		return res, nil
	}
	items, err := s.Proposer.Propose(ctx, orgID, repoID, findings)
	if err != nil {
		return res, fmt.Errorf("propose: %w", err)
	}
	for _, it := range items {
		res.ProposedKeys = append(res.ProposedKeys, it.Key)
	}
	if len(items) > 0 {
		log.Printf("maintenance: proposed %d work item(s) for %s", len(items), name)
	}
	return res, nil
}

// maxTreeDepth bounds the recursive walk. A repository is not expected to
// nest this deeply, and a bound means a pathological or cyclic tree cannot
// spin this loop forever.
const maxTreeDepth = 32

// materialiseDir writes the tree at ref into dir through the git service, so a
// scanner that walks files on disk has files to walk. It reads through the
// platform rather than cloning directly: the service that owns the
// repositories decides what may be read.
func materialiseDir(ctx context.Context, git gitv1.GitServiceClient, repoID uuid.UUID, ref, path, dir string, depth int) error {
	if depth > maxTreeDepth {
		return nil
	}
	tree, err := git.GetTree(ctx, &gitv1.GetTreeRequest{Repo: repoID.String(), Ref: ref, Path: path})
	if err != nil {
		return fmt.Errorf("read tree at %q: %w", path, err)
	}
	for _, entry := range tree.GetEntries() {
		child := entry.GetName()
		if path != "" {
			child = path + "/" + entry.GetName()
		}
		if entry.GetKind() == "tree" {
			if err := materialiseDir(ctx, git, repoID, ref, child, dir, depth+1); err != nil {
				return err
			}
			continue
		}
		clean := filepath.Clean(child)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("refusing to write %q outside the scan directory", child)
		}
		blob, err := git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repoID.String(), Ref: ref, Path: child})
		if err != nil {
			return fmt.Errorf("read %s: %w", child, err)
		}
		full := filepath.Join(dir, clean)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("create directory for %s: %w", child, err)
		}
		if err := os.WriteFile(full, blob.GetContent(), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", child, err)
		}
	}
	return nil
}
