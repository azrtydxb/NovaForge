package maintenance_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/maintenance"
)

func TestGraphScannersUseIndexedEdgesAndRepositoryScope(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := database.Migrate(url, "graph", graph.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := graph.NewStore(pool)
	org, repo := uuid.New(), uuid.New()
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorKind: "user"})
	symbol := func(name string) graph.Node {
		return graph.Node{OrgID: org, Kind: "symbol", Key: repo.String() + ":" + name, Attrs: map[string]string{"name": name, "kind": "function", "dir": "pkg", "path": "pkg/code.go"}}
	}
	used, tested, unused := symbol("Used"), symbol("Tested"), symbol("Unused")
	if err := store.ReplaceFileIndex(ctx, graph.FileIndex{OrgID: org, RepoID: repo, Path: "pkg/code.go", Symbols: []graph.Node{used, tested, unused}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceFileIndex(ctx, graph.FileIndex{OrgID: org, RepoID: repo, Path: "pkg/caller.go", References: []graph.FileReference{{TargetDir: "pkg", TargetName: "Used", Kind: "depends_on"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceFileIndex(ctx, graph.FileIndex{OrgID: org, RepoID: repo, Path: "pkg/code_test.go", References: []graph.FileReference{{TargetDir: "pkg", TargetName: "Tested", Kind: "tested_by"}}}); err != nil {
		t.Fatal(err)
	}
	in := maintenance.ScanInput{OrgID: org, RepoID: repo, Graph: store}
	t.Run("production edge kinds", func(t *testing.T) {
		got, err := maintenance.Scanners["dead_code"](ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !strings.Contains(got[0].Title, unused.Key) {
			t.Fatalf("findings = %+v, want only %s", got, unused.Key)
		}
	})
	foreign := symbol("OnlyInOtherRepository")
	if err := store.ReplaceFileIndex(ctx, graph.FileIndex{OrgID: org, RepoID: uuid.New(), Path: "pkg/code.go", Symbols: []graph.Node{foreign}}); err != nil {
		t.Fatal(err)
	}
	in.ContextDocs = []maintenance.ContextDocRef{{Path: ".novaforge/context/design.md", ReferencedSymbols: []string{used.Key, foreign.Key}}}
	t.Run("documentation repository boundary", func(t *testing.T) {
		got, err := maintenance.Scanners["documentation_drift"](ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Detail != "missing: "+foreign.Key {
			t.Fatalf("findings = %+v, want missing %s", got, foreign.Key)
		}
	})
	t.Run("caller authorization", func(t *testing.T) {
		other := authz.WithScope(context.Background(), authz.Scope{OrgID: uuid.New()})
		for _, kind := range []string{"dead_code", "documentation_drift"} {
			if got, err := maintenance.Scanners[kind](other, in); err == nil {
				t.Errorf("%s accepted a foreign scope: %+v", kind, got)
			}
		}
	})
}
