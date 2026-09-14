package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/analysis"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/maintenance"
	"github.com/novaforge/novaforge/internal/work"
)

// runMaintenanceScanners sweeps every repository on an interval, proposing
// a Work Item for each finding. Nothing executes a fix and nothing starts
// an agent: a proposal is a plain, unassigned Work Item a person decides
// about. Without this the scanners and the proposer both existed, both
// tested, and nothing ever swept anything.
func runMaintenanceScanners(ctx context.Context, workStore *work.Store, git gitv1.GitServiceClient, hmacSecret string, every time.Duration) {
	proposer := &maintenance.Proposer{Work: workStore}
	// The first sweep runs soon after start, not one interval later: every
	// deploy restarts this process, and on a cluster deployed more often than
	// the interval a ticker alone never fired, so nothing was ever proposed.
	first := time.NewTimer(firstSweepDelay)
	defer first.Stop()
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			sweep(ctx, proposer, workStore, git, hmacSecret)
		case <-ticker.C:
			sweep(ctx, proposer, workStore, git, hmacSecret)
		}
	}
}

// firstSweepDelay lets the service's peers come up before the first sweep
// reads from them.
const firstSweepDelay = 2 * time.Minute

// newRepositoryScanner scans one repository on demand, as the caller: its
// credential (forwarded on the git connection) is what reads the repository.
func newRepositoryScanner(workStore *work.Store, git gitv1.GitServiceClient) work.Scanner {
	proposer := &maintenance.Proposer{Work: workStore}
	return func(ctx context.Context, orgID, repoID uuid.UUID) (work.ScanResult, error) {
		repo, err := git.GetRepo(ctx, &gitv1.GetRepoRequest{Name: repoID.String()})
		if err != nil {
			return work.ScanResult{}, fmt.Errorf("resolve repository: %w", err)
		}
		return scanAndPropose(ctx, proposer, git, orgID, repoID, repo.GetRepo().GetName(), repo.GetRepo().GetDefaultBranch())
	}
}

// scanAndPropose is the one path from a repository to proposals, shared by the
// sweep and by a person's scan request.
func scanAndPropose(ctx context.Context, proposer *maintenance.Proposer, git gitv1.GitServiceClient, orgID, repoID uuid.UUID, name, defaultBranch string) (work.ScanResult, error) {
	var res work.ScanResult
	findings, err := scanRepository(ctx, git, orgID, repoID, name, defaultBranch, func(kind string, err error) {
		log.Printf("work-reviews: maintenance: %s scanner on %s: %v", kind, name, err)
		res.ScannerErrors = append(res.ScannerErrors, kind+": "+err.Error())
	})
	if err != nil {
		return res, err
	}
	res.Findings = len(findings)
	if len(findings) == 0 {
		return res, nil
	}
	items, err := proposer.Propose(ctx, orgID, repoID, findings)
	if err != nil {
		return res, fmt.Errorf("propose: %w", err)
	}
	for _, it := range items {
		res.ProposedKeys = append(res.ProposedKeys, it.Key)
	}
	if len(items) > 0 {
		log.Printf("work-reviews: maintenance: proposed %d work item(s) for %s", len(items), name)
	}
	return res, nil
}

// sweep scans every repository of every organization that has work items,
// once. One repository's failure is logged and skipped.
func sweep(ctx context.Context, proposer *maintenance.Proposer, workStore *work.Store, git gitv1.GitServiceClient, hmacSecret string) {
	orgs, err := workStore.OrganizationsWithWork(ctx)
	if err != nil {
		log.Printf("work-reviews: maintenance: list organizations: %v", err)
		return
	}
	for _, orgID := range orgs {
		orgCtx := authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorKind: "service"})
		callCtx, err := withServiceIdentity(orgCtx, hmacSecret, orgID)
		if err != nil {
			log.Printf("work-reviews: maintenance: %v", err)
			continue
		}
		repos, err := git.ListRepos(callCtx, &gitv1.ListReposRequest{})
		if err != nil {
			log.Printf("work-reviews: maintenance: list repos for org %s: %v", orgID, err)
			continue
		}
		for _, r := range repos.GetRepos() {
			repoID, perr := uuid.Parse(r.GetId())
			if perr != nil {
				continue
			}
			if _, serr := scanAndPropose(callCtx, proposer, git, orgID, repoID, r.GetName(), r.GetDefaultBranch()); serr != nil {
				log.Printf("work-reviews: maintenance: scan %s: %v", r.GetName(), serr)
			}
		}
	}
}

// scanRepository checks the repository out into a temporary directory and
// runs every scanner against it. Scanners whose tooling is absent from this
// image report that through onError and are skipped; the remaining
// scanners still run and still report, which is the isolation the scanner
// registry is built around.
func scanRepository(ctx context.Context, git gitv1.GitServiceClient, orgID, repoID uuid.UUID, name, defaultBranch string, onError func(kind string, err error)) ([]maintenance.Finding, error) {
	dir, err := os.MkdirTemp("", "novaforge-maintenance-*")
	if err != nil {
		return nil, fmt.Errorf("create scan directory: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := materialiseTree(ctx, git, repoID, defaultBranch, dir); err != nil {
		return nil, err
	}

	in := maintenance.ScanInput{
		OrgID:     orgID,
		RepoID:    repoID,
		WorkDir:   dir,
		TargetRef: defaultBranch,
		Exec:      analysis.DefaultExec,
		Git:       git,
	}
	return maintenance.RunAll(ctx, in, onError), nil
}

// materialiseTree writes the repository's default-branch tree into dir
// through the git service, so a scanner that walks files on disk has files
// to walk. It reads through the platform rather than cloning directly: the
// service that owns the repositories is the one that decides what may be
// read.
func materialiseTree(ctx context.Context, git gitv1.GitServiceClient, repoID uuid.UUID, ref, dir string) error {
	return materialiseDir(ctx, git, repoID, ref, "", dir, 0)
}

// maxTreeDepth bounds the recursive walk. A repository is not expected to
// nest this deeply, and a bound means a pathological or cyclic tree cannot
// spin this loop forever.
const maxTreeDepth = 32

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
		blob, err := git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repoID.String(), Ref: ref, Path: child})
		if err != nil {
			return fmt.Errorf("read %s: %w", child, err)
		}
		full := filepath.Join(dir, filepath.Clean(child))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("create directory for %s: %w", child, err)
		}
		if err := os.WriteFile(full, blob.GetContent(), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", child, err)
		}
	}
	return nil
}
