package indexing

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/graph"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type goModuleContext struct {
	name, dir, hash, sourcePath string
	complete                    bool
}
type moduleContexts struct {
	modules   map[string]goModuleContext
	workspace bool
}

// Module manifests are source evidence, not the build tool's selected module
// graph. Workspace/replace/cross-module selection stays incomplete until a
// compiler-backed extractor can establish which local source is actually used.
func (idx *Indexer) moduleContexts(ctx context.Context, org, repo uuid.UUID, sha string, changed []string) (*moduleContexts, error) {
	existing, err := idx.existingPaths(ctx, org, repo)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{"go.mod": true, "go.work": true}
	for _, p := range append(existing, changed...) {
		if moduleManifest(p) {
			names[p] = true
		}
	}
	out := &moduleContexts{modules: map[string]goModuleContext{}}
	for name := range names {
		blob, err := idx.Git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repo.String(), Ref: sha, Path: name})
		if status.Code(err) == codes.NotFound {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("module context %s: %w", name, err)
		}
		if path.Base(name) == "go.work" {
			out.workspace = true
			continue
		}
		m := goModuleContext{dir: graphDir(name), hash: graph.SourceDigest(blob.GetContent()), sourcePath: name}
		parsed, err := modfile.Parse(name, blob.GetContent(), nil)
		if err == nil && parsed.Module != nil {
			m.name = parsed.Module.Mod.Path
			// modfile.Parse accepts malformed identities. Match Go's main-module
			// validation, not CheckPath's remote-module rules: local names may
			// legitimately be dotless.
			m.complete = module.CheckImportPath(m.name) == nil && len(parsed.Replace) == 0
		}
		out.modules[m.dir] = m
	}
	return out, nil
}

func moduleManifest(name string) bool {
	return path.Base(name) == "go.mod" || path.Base(name) == "go.work"
}

func (m *moduleContexts) nearest(file string) goModuleContext {
	for dir := graphDir(file); ; dir = graphDir(dir) {
		if module, ok := m.modules[dir]; ok {
			return module
		}
		if dir == "" {
			break
		}
	}
	return goModuleContext{hash: graph.SourceDigest(nil), complete: true}
}

func (m *moduleContexts) edges(file string, parsed File, keys map[*Symbol]string) ([]graph.FileImport, []graph.FileReference, goModuleContext) {
	owner := m.nearest(file)
	owner.complete = owner.complete && !m.workspace
	imports, refs := goEdgesResolved(file, parsed, keys, func(importPath string) string {
		if err := module.CheckImportPath(importPath); err != nil {
			owner.complete = false
			return ""
		}
		local := moduleDir(owner.name, importPath)
		if local == "" {
			// An import that names a different repository module is not proof that
			// Go selected that checkout rather than a required external version.
			for _, other := range m.modules {
				if moduleDir(other.name, importPath) != "" {
					owner.complete = false
				}
			}
			return ""
		}
		target := path.Join(owner.dir, local)
		if target == "." {
			target = ""
		}
		actual := m.nearest(path.Join(target, "_source.go"))
		if actual.dir != owner.dir {
			owner.complete = false
			return ""
		}
		if target == "" {
			return "."
		}
		return strings.TrimPrefix(target, "./")
	})
	return imports, refs, owner
}
