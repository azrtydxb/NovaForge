// Package gitops implements git repository operations, smart-HTTP and SSH
// transports over the git binary. Standard git compatibility is achieved by
// shelling out to git rather than reimplementing the protocol in Go.
package gitops

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// Repo is a bare git repository rooted under an org-scoped directory.
type Repo struct {
	path string
}

// Ref is a branch or tag.
type Ref struct {
	Name string
	SHA  string
	Kind string
}

// Commit is one entry from git log.
type Commit struct {
	SHA         string
	Message     string
	AuthorName  string
	AuthorEmail string
	At          time.Time
}

// TreeEntry is one entry from git ls-tree.
type TreeEntry struct {
	Mode string
	Kind string
	SHA  string
	Name string
	Size int64
}

// run shells out to git with args in dir (empty dir means the current
// working directory) and returns stdout, wrapping any failure with stderr.
func run(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w — %s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// resolvePath validates name and returns the on-disk path for the bare
// repository belonging to orgID under root. The org id segment is what keeps
// one org's repositories unreachable from another's.
func resolvePath(root string, orgID uuid.UUID, name string) (string, error) {
	if !repoNameRe.MatchString(name) {
		return "", fmt.Errorf("invalid repository name %q", name)
	}
	return filepath.Join(root, orgID.String(), name+".git"), nil
}

// Init creates a new bare repository for orgID named name under root.
func Init(root string, orgID uuid.UUID, name string) (Repo, error) {
	path, err := resolvePath(root, orgID, name)
	if err != nil {
		return Repo{}, err
	}
	if _, err := run("", "init", "--bare", "--initial-branch=main", path); err != nil {
		return Repo{}, err
	}
	return Repo{path: path}, nil
}

// Open returns a Repo handle for an existing bare repository. It does not
// verify the repository exists on disk; callers that need that guarantee
// should call a method that touches the repository.
func Open(root string, orgID uuid.UUID, name string) (Repo, error) {
	path, err := resolvePath(root, orgID, name)
	if err != nil {
		return Repo{}, err
	}
	return Repo{path: path}, nil
}

// Path returns the repository's on-disk path.
func (r Repo) Path() string { return r.path }

func (r Repo) refs(kind string) ([]Ref, error) {
	pattern := "refs/heads/"
	refType := "branch"
	if kind == "tags" {
		pattern = "refs/tags/"
		refType = "tag"
	}
	out, err := run("", "--git-dir="+r.path, "for-each-ref",
		"--format=%(refname:short)%00%(objectname)%00%(objecttype)", pattern)
	if err != nil {
		return nil, err
	}
	return parseRefs(out, refType), nil
}

func parseRefs(out []byte, fallbackKind string) []Ref {
	var refs []Ref
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) != 3 {
			continue
		}
		kind := parts[2]
		if kind == "" {
			kind = fallbackKind
		}
		refs = append(refs, Ref{Name: parts[0], SHA: parts[1], Kind: kind})
	}
	return refs
}

// Branches lists the repository's branches.
func (r Repo) Branches() ([]Ref, error) { return r.refs("heads") }

// Tags lists the repository's tags.
func (r Repo) Tags() ([]Ref, error) { return r.refs("tags") }

// Log returns up to limit commits reachable from ref, most recent first.
func (r Repo) Log(ref string, limit int) ([]Commit, error) {
	out, err := run("", "--git-dir="+r.path, "log",
		"--format=%H%x00%s%x00%an%x00%ae%x00%aI",
		"-n", strconv.Itoa(limit), ref)
	if err != nil {
		return nil, err
	}
	var commits []Commit
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) != 5 {
			continue
		}
		at, err := time.Parse(time.RFC3339, parts[4])
		if err != nil {
			return nil, fmt.Errorf("parse commit time %q: %w", parts[4], err)
		}
		commits = append(commits, Commit{
			SHA:         parts[0],
			Message:     parts[1],
			AuthorName:  parts[2],
			AuthorEmail: parts[3],
			At:          at,
		})
	}
	return commits, nil
}

// Tree lists the entries at path within ref. An empty path lists the root.
func (r Repo) Tree(ref, path string) ([]TreeEntry, error) {
	target := ref
	if path != "" {
		target = ref + ":" + path
	}
	out, err := run("", "--git-dir="+r.path, "ls-tree", "-l", target)
	if err != nil {
		return nil, err
	}
	var entries []TreeEntry
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		// format: <mode> SP <type> SP <sha> SP+ <size|-> TAB <name>
		tabIdx := strings.IndexByte(line, '\t')
		if tabIdx < 0 {
			continue
		}
		name := line[tabIdx+1:]
		fields := strings.Fields(line[:tabIdx])
		if len(fields) != 4 {
			continue
		}
		var size int64
		if fields[3] != "-" {
			size, _ = strconv.ParseInt(fields[3], 10, 64)
		}
		entries = append(entries, TreeEntry{
			Mode: fields[0],
			Kind: fields[1],
			SHA:  fields[2],
			Name: name,
			Size: size,
		})
	}
	return entries, nil
}

// Blob returns the raw contents of the file at path within ref.
func (r Repo) Blob(ref, path string) ([]byte, error) {
	return run("", "--git-dir="+r.path, "cat-file", "blob", ref+":"+path)
}

// Diff returns the unified diff between from and to.
func (r Repo) Diff(from, to string) (string, error) {
	return r.diff(from + ".." + to)
}

// DiffSinceMergeBase returns the unified diff of to against the commit where
// it diverged from from — a branch's own change, whatever has landed on from
// since.
func (r Repo) DiffSinceMergeBase(from, to string) (string, error) {
	return r.diff(from + "..." + to)
}

func (r Repo) diff(rangeSpec string) (string, error) {
	out, err := run("", "--git-dir="+r.path, "diff", rangeSpec)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
