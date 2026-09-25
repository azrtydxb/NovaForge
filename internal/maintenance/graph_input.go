package maintenance

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/graph"
)

// graphInput reads through the owning service, never work-reviews' database.
// The manifest comes from the same pinned checkout as the context documents.
func graphInput(ctx context.Context, client graphv1.GraphServiceClient, org, repo uuid.UUID, dir string) (GraphEvidence, error) {
	if client == nil {
		return nil, fmt.Errorf("graph maintenance unavailable: no graph service configured")
	}
	files := map[string]string{}
	modules := map[string]string{}
	err := filepath.WalkDir(dir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("graph manifest refuses symlink %q", rel)
		}
		if entry.Name() == "go.mod" {
			body, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			modules[rel] = graph.SourceDigest(body)
		}
		if !strings.HasSuffix(rel, ".go") {
			return nil
		}
		if len(files) >= 5000 {
			return fmt.Errorf("graph maintenance exceeds 5000 Go files")
		}
		body, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		files[rel] = graph.SourceDigest(body)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("graph maintenance unavailable: no Go source files")
	}
	moduleHashes, modulePaths := map[string]string{}, map[string]string{}
	for file := range files {
		moduleHashes[file], modulePaths[file] = graph.SourceDigest(nil), ""
		for dir := path.Dir(file); ; dir = path.Dir(dir) {
			name := path.Join(dir, "go.mod")
			if hash, ok := modules[name]; ok {
				moduleHashes[file], modulePaths[file] = hash, name
				break
			}
			if dir == "." {
				break
			}
		}
	}
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := client.MaintenanceSnapshot(call, &graphv1.MaintenanceSnapshotRequest{
		RepoId: repo.String(), GoFileHashes: files, GoFileModuleHashes: moduleHashes, GoFileModulePaths: modulePaths,
	})
	if err != nil {
		return nil, fmt.Errorf("graph maintenance unavailable: %w", err)
	}
	out := &snapshotEvidence{org: org, repo: repo, symbols: map[string]bool{}}
	for _, node := range resp.GetSymbols() {
		out.symbols[node.GetKey()] = true
		out.symbols[node.GetAttrs()["path"]+"#"+node.GetAttrs()["name"]] = true
	}
	for _, node := range resp.GetUnreferenced() {
		out.unused = append(out.unused, graph.Node{OrgID: org, Kind: node.GetKind(), Key: node.GetKey(), Attrs: node.GetAttrs()})
	}
	return out, nil
}

type snapshotEvidence struct {
	org, repo uuid.UUID
	symbols   map[string]bool
	unused    []graph.Node
}

func (s *snapshotEvidence) scope(ctx context.Context, org, repo uuid.UUID) error {
	if org != s.org || repo != s.repo {
		return fmt.Errorf("graph snapshot scope mismatch")
	}
	return authz.RequireOrg(ctx, org)
}

func (s *snapshotEvidence) UnreferencedSymbols(ctx context.Context, org, repo uuid.UUID) ([]graph.Node, error) {
	if err := s.scope(ctx, org, repo); err != nil {
		return nil, err
	}
	return s.unused, nil
}

func (s *snapshotEvidence) MissingSymbols(ctx context.Context, org, repo uuid.UUID, symbols []string) ([]string, error) {
	if err := s.scope(ctx, org, repo); err != nil {
		return nil, err
	}
	var missing []string
	for _, symbol := range symbols {
		if !s.symbols[symbol] {
			missing = append(missing, symbol)
		}
	}
	return missing, nil
}
