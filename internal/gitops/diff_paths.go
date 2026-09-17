package gitops

import (
	"fmt"
	"strings"
)

// DiffDetails returns display text and a machine-readable path manifest from
// the same pinned endpoints. Diff headers quote unusual names; parsing them as
// whitespace-separated words silently loses source and leaves renamed files.
func (r Repo) DiffDetails(from, to string, mergeBase bool) (string, []string, error) {
	pin := func(ref string) (string, error) {
		if strings.HasPrefix(ref, "-") {
			return "", fmt.Errorf("invalid diff ref %q", ref)
		}
		out, err := run("", "--git-dir="+r.path, "rev-parse", "--verify", "--end-of-options", ref)
		return strings.TrimSpace(string(out)), err
	}
	from, err := pin(from)
	if err != nil {
		return "", nil, err
	}
	to, err = pin(to)
	if err != nil {
		return "", nil, err
	}
	sep := ".."
	if mergeBase {
		sep = "..."
	}
	unified, err := r.diff(from, sep, to)
	if err != nil {
		return "", nil, err
	}
	// Disable rename detection for the manifest: the old path needs removal
	// just as surely as the destination needs indexing. This also bounds the
	// operation independently of Git's similarity-search configuration.
	out, err := run("", "--git-dir="+r.path, "diff", "--name-only", "--no-renames", "-z", from+sep+to, "--")
	if err != nil {
		return "", nil, err
	}
	if len(out) == 0 {
		return unified, nil, nil
	}
	return unified, strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00"), nil
}
