package gates

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/analysis"
)

// The controller's production lookups over the other services. They lived in
// cmd/gates, where no test could build the controller the deployment runs, so
// every test of a gate joined to a merge used a hand-written RunLookup.

// NewController composes the controller exactly as the gates service runs it:
// runs are resolved through reviews, work and git-platform, gate inputs are
// checked out through git-platform, and proof is recorded back on the run.
func NewController(store *Store, gitClient gitv1.GitServiceClient, reviewsClient reviewsv1.ReviewsServiceClient, workClient workv1.WorkServiceClient, semgrepRules string) *Controller {
	return &Controller{
		Store:      store,
		Git:        gitClient,
		Runs:       NewRunLookup(reviewsClient, workClient, gitClient),
		BuildInput: NewInputBuilder(gitClient, semgrepRules),
		Proof: func(ctx context.Context, runID uuid.UUID, gate, status, detail string) error {
			_, err := reviewsClient.RecordProof(ctx, &reviewsv1.RecordProofRequest{
				RunId: runID.String(), Gate: gate, Status: status, Detail: detail,
			})
			return err
		},
	}
}

// NewRunLookup resolves a RunHead by asking the reviews and work services,
// and the git-platform service for the target branch's current head SHA —
// the gates schema never reads their tables directly.
func NewRunLookup(reviewsClient reviewsv1.ReviewsServiceClient, workClient workv1.WorkServiceClient, gitClient gitv1.GitServiceClient) RunLookup {
	return func(ctx context.Context, runID uuid.UUID) (RunHead, error) {
		runResp, err := reviewsClient.GetRun(ctx, &reviewsv1.GetRunRequest{Id: runID.String()})
		if err != nil {
			return RunHead{}, fmt.Errorf("get run %s: %w", runID, err)
		}
		run := runResp.GetRun()

		orgID, err := uuid.Parse(run.GetOrgId())
		if err != nil {
			return RunHead{}, fmt.Errorf("run %s has invalid org_id: %w", runID, err)
		}
		repoID, err := uuid.Parse(run.GetRepoId())
		if err != nil {
			return RunHead{}, fmt.Errorf("run %s has invalid repo_id: %w", runID, err)
		}

		var requiredGates []string
		if run.GetWorkItemId() != "" {
			itemResp, err := workClient.GetItem(ctx, &workv1.GetItemRequest{Id: run.GetWorkItemId()})
			if err != nil {
				return RunHead{}, fmt.Errorf("get work item %s: %w", run.GetWorkItemId(), err)
			}
			requiredGates = itemResp.GetItem().GetRequiredGates()
		}

		repoName, err := resolveRepoName(ctx, gitClient, repoID)
		if err != nil {
			return RunHead{}, err
		}

		branch := strings.TrimPrefix(run.GetTargetRef(), "refs/heads/")
		branchesResp, err := gitClient.ListBranches(ctx, &gitv1.ListBranchesRequest{Repo: repoName})
		if err != nil {
			return RunHead{}, fmt.Errorf("list branches for %s: %w", repoName, err)
		}
		var headSHA string
		for _, ref := range branchesResp.GetRefs() {
			if ref.GetName() == branch {
				headSHA = ref.GetSha()
				break
			}
		}
		if headSHA == "" {
			return RunHead{}, fmt.Errorf("branch %q not found in repo %s", branch, repoName)
		}

		return RunHead{
			OrgID:         orgID,
			RepoID:        repoID,
			TargetRef:     run.GetTargetRef(),
			HeadSHA:       headSHA,
			WorkItemGates: requiredGates,
		}, nil
	}
}

// resolveRepoName looks up a repository's name from its id — the git
// transport RPCs (GetTree, GetBlob, ListBranches) address a repository by
// name within the caller's organization, not by id.
func resolveRepoName(ctx context.Context, gitClient gitv1.GitServiceClient, repoID uuid.UUID) (string, error) {
	reposResp, err := gitClient.ListRepos(ctx, &gitv1.ListReposRequest{})
	if err != nil {
		return "", fmt.Errorf("list repos: %w", err)
	}
	for _, r := range reposResp.GetRepos() {
		if r.GetId() == repoID.String() {
			return r.GetName(), nil
		}
	}
	return "", fmt.Errorf("repo %s not found", repoID)
}

// NewInputBuilder materializes the run's target commit into a fresh
// temporary workspace by walking GetTree/GetBlob recursively, so a gate
// runner running analysis tools sees a real checkout on disk. The
// workspace is removed once ctx (the RPC's own context, which stays live
// for the whole Evaluate call) is done. semgrepRules is the SAST ruleset
// path the security gate reads.
func NewInputBuilder(gitClient gitv1.GitServiceClient, semgrepRules string) InputBuilder {
	return func(ctx context.Context, runID uuid.UUID, head RunHead, gate string, params map[string]any) (Input, error) {
		repoName, err := resolveRepoName(ctx, gitClient, head.RepoID)
		if err != nil {
			return Input{}, err
		}

		workdir, err := os.MkdirTemp("", "nf-gate-"+gate+"-*")
		if err != nil {
			return Input{}, fmt.Errorf("create workspace: %w", err)
		}
		go func() {
			<-ctx.Done()
			_ = os.RemoveAll(workdir)
		}()

		if err := materializeTree(ctx, gitClient, repoName, head.HeadSHA, "", workdir); err != nil {
			return Input{}, fmt.Errorf("materialize workspace for %s@%s: %w", repoName, head.HeadSHA, err)
		}

		return Input{
			OrgID:     head.OrgID,
			RepoID:    head.RepoID,
			RunID:     runID,
			WorkDir:   workdir,
			TargetSHA: head.HeadSHA,
			SourceSHA: head.HeadSHA,
			Params:    params,
			Exec:      analysis.DefaultExec,
			SASTRules: semgrepRules,
		}, nil
	}
}

// materializeTree recursively checks out ref's tree at path into destDir by
// walking GetTree and fetching each blob's content with GetBlob.
func materializeTree(ctx context.Context, gitClient gitv1.GitServiceClient, repo, ref, path, destDir string) error {
	treeResp, err := gitClient.GetTree(ctx, &gitv1.GetTreeRequest{Repo: repo, Ref: ref, Path: path})
	if err != nil {
		return fmt.Errorf("get tree %q: %w", path, err)
	}
	for _, entry := range treeResp.GetEntries() {
		entryPath := entry.GetName()
		if path != "" {
			entryPath = path + "/" + entry.GetName()
		}
		destPath := filepath.Join(destDir, entryPath)
		switch entry.GetKind() {
		case "tree":
			if err := os.MkdirAll(destPath, 0o755); err != nil {
				return fmt.Errorf("mkdir %q: %w", entryPath, err)
			}
			if err := materializeTree(ctx, gitClient, repo, ref, entryPath, destDir); err != nil {
				return err
			}
		case "blob":
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return fmt.Errorf("mkdir parent of %q: %w", entryPath, err)
			}
			blobResp, err := gitClient.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repo, Ref: ref, Path: entryPath})
			if err != nil {
				return fmt.Errorf("get blob %q: %w", entryPath, err)
			}
			if err := os.WriteFile(destPath, blobResp.GetContent(), 0o644); err != nil {
				return fmt.Errorf("write %q: %w", entryPath, err)
			}
		}
	}
	return nil
}
