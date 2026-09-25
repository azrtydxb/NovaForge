package gates

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// NewController composes the controller exactly as the gates service runs it:
// runs are resolved through reviews, work and git-platform, gate inputs are
// checked out through git-platform, and proof is recorded back on the run. It
// lives here, not in cmd/gates, so a test can build the controller the
// deployment runs.
func NewController(store *Store, gitClient gitv1.GitServiceClient, reviewsClient reviewsv1.ReviewsServiceClient, workClient workv1.WorkServiceClient, semgrepRules string, options ...ControllerOption) *Controller {
	var config controllerConfig
	for _, option := range options {
		option(&config)
	}
	return &Controller{
		Store:      store,
		Git:        gitClient,
		Runs:       NewServiceRunLookup(reviewsClient, workClient, gitClient),
		BuildInput: NewWorkspaceInputBuilder(gitClient, semgrepRules, config.sandbox),
		Proof: func(ctx context.Context, runID uuid.UUID, gate, status, detail string) error {
			if config.proofContext == nil {
				return fmt.Errorf("gate proof service authentication is not configured")
			}
			proofCtx, err := config.proofContext(ctx)
			if err != nil {
				return err
			}
			_, err = reviewsClient.RecordProof(proofCtx, &reviewsv1.RecordProofRequest{
				RunId: runID.String(), Gate: gate, Status: status, Detail: detail,
			})
			return err
		},
	}
}

// NewServiceRunLookup resolves a RunHead by asking the reviews and work
// services, and git-platform for the branch heads — the gates schema never
// reads their tables directly. It lives here rather than in cmd/gates so the
// tests that join the controller to a real merge use the lookup production
// uses: a copy in a test is where "both sides correct, nothing connected"
// hides. work may be nil for a caller whose runs carry no Work Item.
func NewServiceRunLookup(reviewsClient reviewsv1.ReviewsServiceClient, workClient workv1.WorkServiceClient, gitClient gitv1.GitServiceClient) RunLookup {
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
			if workClient == nil {
				return RunHead{}, fmt.Errorf("run %s carries a work item but no work client is configured", runID)
			}
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
		branchesResp, err := gitClient.ListBranches(ctx, &gitv1.ListBranchesRequest{Repo: repoName})
		if err != nil {
			return RunHead{}, fmt.Errorf("list branches for %s: %w", repoName, err)
		}
		heads := map[string]string{}
		for _, ref := range branchesResp.GetRefs() {
			heads[ref.GetName()] = ref.GetSha()
		}
		target := strings.TrimPrefix(run.GetTargetRef(), "refs/heads/")
		if heads[target] == "" {
			return RunHead{}, fmt.Errorf("branch %q not found in repo %s", target, repoName)
		}
		// The gates judge the change, so the head they evaluate is the source
		// branch's. This used to be the target's: every gate ran against main
		// as it already was, so a change with failing tests passed its tests
		// gate and merged. Definitions are still read from the target (see
		// Resolve), which is what keeps a change from weakening its own gates.
		source := strings.TrimPrefix(run.GetSourceRef(), "refs/heads/")
		headSHA := heads[source]
		if headSHA == "" {
			return RunHead{}, fmt.Errorf("source branch %q not found in repo %s", source, repoName)
		}

		var authorID uuid.UUID
		if raw := run.GetAuthorId(); raw != "" {
			authorID, _ = uuid.Parse(raw)
		}
		return RunHead{
			OrgID:         orgID,
			RepoID:        repoID,
			TargetRef:     run.GetTargetRef(),
			TargetSHA:     heads[target],
			HeadSHA:       headSHA,
			WorkItemGates: requiredGates,
			SourceRef:     run.GetSourceRef(),
			AuthorID:      authorID,
			AuthorKind:    run.GetAuthorKind(),
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

// NewWorkspaceInputBuilder materializes the commit a gate judges into a fresh
// temporary workspace by walking GetTree/GetBlob recursively, so a gate
// runner running analysis tools sees a real checkout on disk. The workspace
// is removed once ctx (the RPC's own context, which stays live for the whole
// Evaluate call) is done. semgrepRules is the security gate's ruleset path.
func NewWorkspaceInputBuilder(gitClient gitv1.GitServiceClient, semgrepRules string, sandboxes ...*AnalysisSandbox) InputBuilder {
	return func(ctx context.Context, runID uuid.UUID, head RunHead, gate string, params map[string]any) (Input, error) {
		if len(sandboxes) != 1 || sandboxes[0] == nil {
			return Input{}, fmt.Errorf("isolated analysis sandbox is not configured")
		}
		if !gateRevision.MatchString(head.TargetSHA) {
			return Input{}, fmt.Errorf("immutable gate policy revision is missing")
		}
		if head.OrgID == uuid.Nil {
			return Input{}, fmt.Errorf("gate organization is missing")
		}
		if err := authz.RequireOrg(ctx, head.OrgID); err != nil {
			return Input{}, err
		}
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
			_ = os.RemoveAll(workdir)
			return Input{}, fmt.Errorf("materialize workspace for %s@%s: %w", repoName, head.HeadSHA, err)
		}

		run, err := sandboxes[0].bind(ctx, workdir, runID, head)
		if err != nil {
			_ = os.RemoveAll(workdir)
			return Input{}, err
		}

		sandboxExec := run
		run = func(execCtx context.Context, dir, name string, args ...string) ([]byte, int, error) {
			// Git history is read through its owner RPC at the frozen revisions,
			// not reconstructed in the tenant pod or queried by a local process.
			if name != "git" {
				return sandboxExec(execCtx, dir, name, args...)
			}
			if err := authz.RequireOrg(execCtx, head.OrgID); err != nil {
				return nil, 0, err
			}
			if dir != workdir || len(args) != 2 || args[0] != "show" {
				return nil, 0, fmt.Errorf("unsupported gate Git read")
			}
			sha, path, ok := strings.Cut(args[1], ":")
			if !ok || (sha != head.HeadSHA && sha != head.TargetSHA) || path != openapiSpecPath {
				return nil, 0, fmt.Errorf("unbound gate Git read")
			}
			if _, err := gitClient.GetTree(execCtx, &gitv1.GetTreeRequest{Repo: repoName, Ref: sha}); err != nil {
				return nil, 0, fmt.Errorf("verify API evidence revision: %w", err)
			}
			blob, err := gitClient.GetBlob(execCtx, &gitv1.GetBlobRequest{Repo: repoName, Ref: sha, Path: path})
			if status.Code(err) == codes.NotFound {
				return nil, 44, nil
			}
			if err != nil {
				return nil, 0, err
			}
			if len(blob.GetContent()) > sandboxOutputLimit {
				return nil, 0, fmt.Errorf("OpenAPI evidence exceeds bound")
			}
			return blob.GetContent(), 0, nil
		}
		return Input{
			PolicySHA: head.TargetSHA,
			OrgID:     head.OrgID,
			RepoID:    head.RepoID,
			RunID:     runID,
			WorkDir:   workdir,
			TargetSHA: head.HeadSHA,
			SourceSHA: head.HeadSHA,
			Params:    params,
			Exec:      run,
			SASTRules: semgrepRules,
		}, nil
	}
}

// materializeTree recursively checks out ref's tree at path into destDir by
// walking GetTree and fetching each blob's content with GetBlob.
func materializeTree(ctx context.Context, gitClient gitv1.GitServiceClient, repo, ref, path, destDir string) error {
	budget := &gateTreeBudget{}
	return materializeGateTree(ctx, gitClient, repo, ref, path, destDir, budget)
}

type gateTreeBudget struct{ entries, bytes int }

func materializeGateTree(ctx context.Context, gitClient gitv1.GitServiceClient, repo, ref, path, destDir string, budget *gateTreeBudget) error {
	if strings.Count(path, "/") > 128 {
		return fmt.Errorf("gate source tree exceeds depth bound")
	}
	treeResp, err := gitClient.GetTree(ctx, &gitv1.GetTreeRequest{Repo: repo, Ref: ref, Path: path})
	if err != nil {
		return fmt.Errorf("get tree %q: %w", path, err)
	}
	for _, entry := range treeResp.GetEntries() {
		budget.entries++
		if budget.entries > 10000 {
			return fmt.Errorf("gate source tree exceeds entry bound")
		}
		if entry.GetName() == "" || entry.GetName() == "." || entry.GetName() == ".." || strings.ContainsAny(entry.GetName(), "/\\") {
			return fmt.Errorf("invalid gate tree entry name")
		}
		entryPath := entry.GetName()
		if path != "" {
			entryPath = path + "/" + entry.GetName()
		}
		destPath := filepath.Join(destDir, entryPath)
		// A tree entry name comes from the repository, which the change under
		// review controls; one that escapes the workspace must not be written.
		if rel, err := filepath.Rel(destDir, destPath); err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("tree entry %q escapes the workspace", entryPath)
		}
		switch entry.GetKind() {
		case "tree":
			if err := os.MkdirAll(destPath, 0o755); err != nil {
				return fmt.Errorf("mkdir %q: %w", entryPath, err)
			}
			if err := materializeGateTree(ctx, gitClient, repo, ref, entryPath, destDir, budget); err != nil {
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
			budget.bytes += len(blobResp.GetContent())
			if budget.bytes > sandboxSourceLimit {
				return fmt.Errorf("gate source tree exceeds byte bound")
			}
			if err := os.WriteFile(destPath, blobResp.GetContent(), 0o644); err != nil {
				return fmt.Errorf("write %q: %w", entryPath, err)
			}
		default:
			return fmt.Errorf("unsupported gate tree entry kind %q", entry.GetKind())
		}
	}
	return nil
}
