// Package semanticindex validates semantic evidence produced in an isolated,
// revision-pinned workspace. It never launches tools or reads host source paths.
package semanticindex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	MaxFiles         = 10000
	MaxSourceBytes   = 32 << 20
	MaxFileBytes     = 2 << 20
	MaxArtifactBytes = 32 << 20
	MaxOccurrences   = 200000
)

// Snapshot is supplied by the authenticated indexing controller, not by the
// repository. Files includes build configuration as well as source: changing a
// compiler option must invalidate semantic evidence even when source is unchanged.
// The caller must not mutate Files during ingestion. RootURI names the isolated
// tool workspace; it grants no authority to read that path on this service.
type Snapshot struct {
	OrgID, RepoID uuid.UUID
	Revision      string
	RootURI       string
	// ExecutionDigest hashes a controller-owned manifest of immutable tool image,
	// executable/version, argv, working directory, build-affecting environment,
	// and dependency/toolchain inputs outside Files. Persist that manifest beside
	// the result. A source-only hash cannot distinguish GOOS, build tags, GOWORK,
	// local replacements, compiler options or dependency versions.
	ExecutionDigest string
	Files           map[string][]byte
}

// RunEvidence is controller-owned, never repository-uploaded metadata. For SCIP
// it attests a successful isolated execution; for a live LSP session it attests
// the pinned launch context (the controller must also require successful session
// and process completion before publishing). SCIP carries no revision/content
// hash of its own, so trusting an uploaded attestation would authenticate stale data.
type RunEvidence struct {
	Revision, SnapshotDigest, ExecutionDigest, Tool, ToolVersion string
}

func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func validPath(p string) bool {
	return p != "" && p != "." && !strings.HasPrefix(p, "/") && path.Clean(p) == p &&
		p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\x00\\") && utf8.ValidString(p)
}

func rootURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || u.Host != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		!strings.HasPrefix(u.Path, "/") || strings.ContainsAny(u.Path, "\x00\\") || path.Clean(u.Path) != u.Path {
		return nil, fmt.Errorf("root must be a canonical absolute file URI")
	}
	return u, nil
}

// Digest validates bounds and binds the whole manifest to tenant, repository and
// full Git object ID. Length-delimited JSON avoids ambiguous path/content joins.
func (s Snapshot) Digest() (string, error) {
	if s.OrgID == uuid.Nil || s.RepoID == uuid.Nil {
		return "", fmt.Errorf("organization and repository are required")
	}
	if len(s.Revision) != 40 && len(s.Revision) != 64 {
		return "", fmt.Errorf("revision must be a full Git object ID")
	}
	if _, err := hex.DecodeString(s.Revision); err != nil || strings.ToLower(s.Revision) != s.Revision || strings.Trim(s.Revision, "0") == "" {
		return "", fmt.Errorf("invalid revision")
	}
	if _, err := rootURL(s.RootURI); err != nil {
		return "", err
	}
	if len(s.ExecutionDigest) != 64 || strings.ToLower(s.ExecutionDigest) != s.ExecutionDigest {
		return "", fmt.Errorf("execution context SHA-256 is required")
	}
	if _, err := hex.DecodeString(s.ExecutionDigest); err != nil || strings.Trim(s.ExecutionDigest, "0") == "" {
		return "", fmt.Errorf("invalid execution context SHA-256")
	}
	if len(s.Files) == 0 || len(s.Files) > MaxFiles {
		return "", fmt.Errorf("snapshot file count outside 1..%d", MaxFiles)
	}
	type entry struct{ Path, Hash string }
	entries := make([]entry, 0, len(s.Files))
	total := 0
	for p, b := range s.Files {
		if !validPath(p) {
			return "", fmt.Errorf("invalid snapshot path %q", p)
		}
		total += len(b)
		if len(b) > MaxFileBytes || total > MaxSourceBytes {
			return "", fmt.Errorf("snapshot source limit exceeded at %q", p)
		}
		entries = append(entries, entry{p, digest(b)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	b, err := json.Marshal(struct {
		Org, Repo       uuid.UUID
		Revision        string
		ExecutionDigest string
		Files           []entry
	}{s.OrgID, s.RepoID, s.Revision, s.ExecutionDigest, entries})
	if err != nil {
		return "", err
	}
	return digest(b), nil
}

func (s Snapshot) validateEvidence(e RunEvidence) error {
	d, err := s.Digest()
	if err != nil {
		return err
	}
	if e.Revision != s.Revision || e.SnapshotDigest != d || e.ExecutionDigest != s.ExecutionDigest || e.Tool == "" || e.ToolVersion == "" {
		return fmt.Errorf("semantic evidence does not match snapshot or lacks tool provenance")
	}
	return nil
}
