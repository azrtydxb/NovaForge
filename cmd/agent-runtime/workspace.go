package main

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/workspace"
)

// runCommandTimeout bounds one workspace.run command, so a hung test cannot
// hold a run past its budget's next check.
const runCommandTimeout = 10 * time.Minute

// podWorkspace satisfies tools.Workspace over exec into the run's workspace
// pod, whose repository copy lives at workspace.Root.
type podWorkspace struct {
	provisioner *workspace.Provisioner
	runID       uuid.UUID
}

func (w *podWorkspace) abs(p string) string { return path.Join(workspace.Root, p) }

func (w *podWorkspace) WriteFile(ctx context.Context, p string, content []byte) error {
	res, err := w.provisioner.Exec(ctx, w.runID,
		[]string{"sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", w.abs(p)},
		strings.NewReader(string(content)))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("write %s: exit %d: %s", p, res.ExitCode, res.Stderr)
	}
	return nil
}

func (w *podWorkspace) ReadFile(ctx context.Context, p string) ([]byte, error) {
	res, err := w.provisioner.Exec(ctx, w.runID, []string{"cat", w.abs(p)}, nil)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("read %s: %s", p, strings.TrimSpace(string(res.Stderr)))
	}
	return res.Stdout, nil
}

func (w *podWorkspace) Run(ctx context.Context, command string) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, runCommandTimeout)
	defer cancel()
	res, err := w.provisioner.Exec(ctx, w.runID,
		[]string{"sh", "-c", "cd " + workspace.Root + " && " + command + " 2>&1"}, nil)
	if err != nil {
		return "", 0, err
	}
	return string(res.Stdout) + string(res.Stderr), res.ExitCode, nil
}

// seedWorkspace copies the repository's default branch into the workspace, so
// the agent's checks run against the real code and its edits land beside it.
// The workspace has no network and cannot clone; the tree is read through the
// git service, which decides what the run may read, and streamed in as a tar.
func seedWorkspace(ctx context.Context, git gitv1.GitServiceClient, provisioner *workspace.Provisioner, runID, repoID uuid.UUID) (string, error) {
	repo, err := git.GetRepo(ctx, &gitv1.GetRepoRequest{Name: repoID.String()})
	if err != nil {
		return "", fmt.Errorf("resolve repository: %w", err)
	}
	ref := repo.GetRepo().GetDefaultBranch()
	if ref == "" {
		ref = "main"
	}

	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		err := writeTree(ctx, git, repoID.String(), ref, "", tw, 0)
		if err == nil {
			err = tw.Close()
		}
		_ = pw.CloseWithError(err)
	}()
	res, err := provisioner.Exec(ctx, runID, []string{"tar", "-x", "-C", workspace.Root}, pr)
	_ = pr.Close()
	if err != nil {
		return "", fmt.Errorf("unpack repository into workspace: %w", err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("unpack repository into workspace: tar exit %d: %s", res.ExitCode, res.Stderr)
	}
	// The copy is committed once, locally, so "git status" and "git diff" in
	// workspace.run show exactly what the agent changed. Without it the
	// workspace was a bare file tree, and agents spent their first steps
	// running "git init" and hunting for a remote that the network policy
	// would never let them reach.
	res, err = provisioner.Exec(ctx, runID, []string{"sh", "-c",
		`cd "$1" && git init -q -b "$2" && git add -A && ` +
			`git -c user.name=NovaForge -c user.email=workspace@novaforge.local commit -q --allow-empty -m "$2 at the start of this run"`,
		"sh", workspace.Root, ref}, nil)
	if err != nil {
		return "", fmt.Errorf("record workspace baseline: %w", err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("record workspace baseline: git exit %d: %s", res.ExitCode, res.Stderr)
	}
	return ref, nil
}

// maxSeedDepth bounds the tree walk against a pathological repository.
const maxSeedDepth = 32

func writeTree(ctx context.Context, git gitv1.GitServiceClient, repo, ref, dir string, tw *tar.Writer, depth int) error {
	if depth > maxSeedDepth {
		return nil
	}
	tree, err := git.GetTree(ctx, &gitv1.GetTreeRequest{Repo: repo, Ref: ref, Path: dir})
	if err != nil {
		// An empty repository has no tree at its default branch yet; the
		// workspace simply starts empty.
		if depth == 0 && status.Code(err) == codes.NotFound {
			return nil
		}
		return fmt.Errorf("read tree %q: %w", dir, err)
	}
	for _, e := range tree.GetEntries() {
		p := e.GetName()
		if dir != "" {
			p = dir + "/" + e.GetName()
		}
		switch e.GetKind() {
		case "tree":
			if err := tw.WriteHeader(&tar.Header{Name: p + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
				return err
			}
			if err := writeTree(ctx, git, repo, ref, p, tw, depth+1); err != nil {
				return err
			}
		case "blob":
			blob, err := git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repo, Ref: ref, Path: p})
			if err != nil {
				return fmt.Errorf("read %s: %w", p, err)
			}
			mode := int64(0o644)
			if e.GetMode() == "100755" {
				mode = 0o755
			}
			if err := tw.WriteHeader(&tar.Header{Name: p, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(blob.GetContent()))}); err != nil {
				return err
			}
			if _, err := tw.Write(blob.GetContent()); err != nil {
				return err
			}
		}
	}
	return nil
}
