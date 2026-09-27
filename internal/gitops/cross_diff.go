package gitops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// diffAcross loads two authorized local repositories into a disposable bare
// store. Fetching fork objects into the parent during a read would let a diff
// change what later SHA-based reads can reach. Neither original is modified.
func (r Repo) diffAcross(ctx context.Context, source Repo, from, to string, mergeBase bool) (string, []string, error) {
	git := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("cross-repository diff: %w: %s", err, out)
		}
		return strings.TrimSpace(string(out)), nil
	}
	pin := func(repo Repo, ref string) (string, error) {
		if ref == "" || strings.HasPrefix(ref, "-") {
			return "", fmt.Errorf("invalid diff ref")
		}
		return git("--git-dir="+repo.Path(), "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	}
	fromSHA, err := pin(r, from)
	if err != nil {
		return "", nil, err
	}
	toSHA, err := pin(source, to)
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "nf-cross-diff-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(dir)
	if _, err := git("init", "--bare", "--quiet", dir); err != nil {
		return "", nil, err
	}
	for _, item := range []struct{ path, sha string }{{r.Path(), fromSHA}, {source.Path(), toSHA}} {
		if _, err := git("--git-dir="+dir, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--", item.path, item.sha); err != nil {
			return "", nil, err
		}
	}
	return (Repo{path: dir}).DiffDetails(fromSHA, toSHA, mergeBase)
}
