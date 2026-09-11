package tools

import (
	"context"
	"encoding/json"
	"fmt"

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
	SHA string `json:"sha"`
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
	sha, err := rt.Git.Commit(ctx, args.Repo, args.Branch, args.Message, args.Files)
	if err != nil {
		return nil, fmt.Errorf("git.commit: %w", err)
	}
	return json.Marshal(gitCommitResult{SHA: sha})
}
