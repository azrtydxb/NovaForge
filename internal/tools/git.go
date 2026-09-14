package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/novaforge/novaforge/internal/capability"
)

type gitDiffArgs struct {
	Repo string `json:"repo"`
	From string `json:"from"`
	To   string `json:"to"`
}

type gitDiffResult struct {
	Unified string `json:"unified"`
}

type gitCommitArgs struct {
	Repo    string            `json:"repo"`
	Branch  string            `json:"branch"`
	Message string            `json:"message"`
	Files   map[string]string `json:"files"`
}

type gitCommitResult struct {
	SHA   string   `json:"sha"`
	Files []string `json:"files"`
}

const refHeadsPrefix = "refs/heads/"

func registerGitTools(r *Registry) {
	r.Register("git.diff", gitDiffHandler)
	r.registerWithCap("git.commit", gitCommitCapCheck, gitCommitHandler)
}

func gitDiffHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var args gitDiffArgs
	if err := unmarshalArgs("git.diff", argsJSON, &args); err != nil {
		return nil, err
	}
	if rt.Git == nil {
		return nil, fmt.Errorf("git.diff: no git client configured")
	}
	unified, err := rt.Git.Diff(ctx, args.Repo, args.From, args.To)
	if err != nil {
		return nil, fmt.Errorf("git.diff: %w", err)
	}
	return json.Marshal(gitDiffResult{Unified: unified})
}

// gitCommitCapCheck routes git.commit through capability.CanWriteRef, the
// same function the git HTTP and SSH transports call, so a tool call
// refuses an out-of-scope branch identically to those transports.
func gitCommitCapCheck(rt Runtime, argsJSON []byte) error {
	var args gitCommitArgs
	if err := unmarshalArgs("git.commit", argsJSON, &args); err != nil {
		return err
	}
	return capability.CanWriteRef(rt.Grant, refHeadsPrefix+args.Branch)
}

func gitCommitHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var args gitCommitArgs
	if err := unmarshalArgs("git.commit", argsJSON, &args); err != nil {
		return nil, err
	}
	if rt.Git == nil {
		return nil, fmt.Errorf("git.commit: no git client configured")
	}
	files := args.Files
	var fromWorkspace []string
	if len(files) == 0 {
		// No files given: commit what this run staged in its workspace, read
		// back from the workspace so the commit is exactly what was there when
		// the agent ran its checks.
		fromWorkspace = rt.staged.list()
		if len(fromWorkspace) == 0 {
			return nil, fmt.Errorf("git.commit: no files given and nothing staged with workspace.write_file")
		}
		if rt.Workspace == nil {
			return nil, fmt.Errorf("git.commit: files are staged but this run has no workspace to read them from")
		}
		files = make(map[string]string, len(fromWorkspace))
		for _, p := range fromWorkspace {
			content, err := rt.Workspace.ReadFile(ctx, p)
			if err != nil {
				return nil, fmt.Errorf("git.commit: read staged %s: %w", p, err)
			}
			files[p] = string(content)
		}
	}
	sha, err := rt.Git.Commit(ctx, args.Repo, args.Branch, args.Message, files)
	if err != nil {
		return nil, fmt.Errorf("git.commit: %w", err)
	}
	rt.staged.clear(fromWorkspace)
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return json.Marshal(gitCommitResult{SHA: sha, Files: paths})
}
