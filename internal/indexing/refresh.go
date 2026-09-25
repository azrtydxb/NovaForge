package indexing

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type inventoryClient interface {
	ListOrganizationsWithRepositories(context.Context, *gitv1.ListOrganizationsWithRepositoriesRequest, ...grpc.CallOption) (*gitv1.ListOrganizationsWithRepositoriesResponse, error)
	ListRepos(context.Context, *gitv1.ListReposRequest, ...grpc.CallOption) (*gitv1.ListReposResponse, error)
}

// Refresh reconciles idle/legacy repositories without needing a new push. The
// inventory RPC exposes IDs only to the platform token; every subsequent read
// gets a fresh org-scoped token. No cross-schema inventory query is permitted.
func (idx *Indexer) Refresh(ctx context.Context) error {
	git, ok := idx.Git.(inventoryClient)
	if !ok {
		return errors.New("index refresh requires git inventory RPCs")
	}
	tok, err := svcauth.MintPlatform(idx.HMACSecret, "indexer", svcauth.DefaultTTL)
	if err != nil {
		return err
	}
	call, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+tok)), 30*time.Second)
	orgs, err := git.ListOrganizationsWithRepositories(call, &gitv1.ListOrganizationsWithRepositoriesRequest{})
	cancel()
	if err != nil {
		return fmt.Errorf("index inventory: %w", err)
	}
	var failures []error
	for _, raw := range orgs.GetOrgIds() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		org, err := uuid.Parse(raw)
		if err != nil || org == uuid.Nil {
			failures = append(failures, fmt.Errorf("invalid inventory organization %q", raw))
			continue
		}
		tok, err := svcauth.Mint(idx.HMACSecret, "indexer", org, svcauth.DefaultTTL)
		if err != nil {
			return err
		}
		orgCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+tok, "x-novaforge-org", org.String()))
		call, cancel := context.WithTimeout(orgCtx, 30*time.Second)
		repos, err := git.ListRepos(call, &gitv1.ListReposRequest{})
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("index inventory org %s: %w", org, err))
			continue
		}
		for _, r := range repos.GetRepos() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			repo, err := uuid.Parse(r.GetId())
			if err != nil || repo == uuid.Nil || r.GetOrgId() != org.String() {
				failures = append(failures, fmt.Errorf("invalid repository inventory for org %s", org))
				continue
			}
			// Large organizations may take longer than one credential's TTL.
			// Mint again for each repository rather than reusing the inventory token.
			tok, err := svcauth.Mint(idx.HMACSecret, "indexer", org, svcauth.DefaultTTL)
			if err != nil {
				return err
			}
			orgCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+tok, "x-novaforge-org", org.String()))
			ref := "refs/heads/" + r.GetDefaultBranch()
			call, cancel := context.WithTimeout(orgCtx, 30*time.Second)
			heads, err := idx.Git.ListCommits(call, &gitv1.ListCommitsRequest{Repo: repo.String(), Ref: ref, Limit: 1})
			cancel()
			// Empty or concurrently deleted repositories have no head to refresh.
			if status.Code(err) == codes.NotFound {
				continue
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("refresh head %s: %w", repo, err))
				continue
			}
			if len(heads.GetCommits()) == 0 {
				continue
			}
			// HandlePush re-resolves the head while fenced; this preliminary read
			// only skips empty repositories. A bounded attempt cannot stall inventory.
			call, cancel = context.WithTimeout(ctx, 10*time.Minute)
			err = idx.HandlePush(call, events.PushEvent{OrgID: org, RepoID: repo, Ref: ref, NewSHA: heads.GetCommits()[0].GetSha()})
			cancel()
			if err != nil {
				failures = append(failures, fmt.Errorf("refresh repository %s: %w", repo, err))
			}
		}
	}
	return errors.Join(failures...)
}

func (idx *Indexer) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		if err := idx.Refresh(ctx); err != nil && ctx.Err() == nil {
			log.Printf("indexing: refresh: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
