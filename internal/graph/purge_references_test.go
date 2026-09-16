package graph_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/graph"
)

func TestGraphPurgeRemovesOnlyOwnedFileReferences(t *testing.T) {
	store := newStore(t)
	org, otherOrg := uuid.New(), uuid.New()
	first, second, otherRepo := uuid.New(), uuid.New(), uuid.New()
	ctx, otherCtx := scopedCtx(org, uuid.New()), scopedCtx(otherOrg, uuid.New())
	for _, fi := range []graph.FileIndex{
		{OrgID: org, RepoID: first, Path: "a.go"},
		{OrgID: org, RepoID: second, Path: "b.go"},
		{OrgID: otherOrg, RepoID: otherRepo, Path: "c.go"},
	} {
		fi.References = []graph.FileReference{{TargetName: "Used", Kind: "depends_on"}}
		scoped := ctx
		if fi.OrgID == otherOrg {
			scoped = otherCtx
		}
		if err := store.ReplaceFileIndex(scoped, fi); err != nil {
			t.Fatal(err)
		}
	}
	check := func(repo uuid.UUID, want int) {
		t.Helper()
		var count int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM graph.file_references WHERE repo_id=$1`, repo).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Errorf("repo %s references=%d, want %d", repo, count, want)
		}
	}
	if err := store.PurgeRepository(otherCtx, first); err != nil {
		t.Fatal(err)
	}
	check(first, 1)
	if err := store.PurgeRepository(ctx, first); err != nil {
		t.Fatal(err)
	}
	check(first, 0)
	check(second, 1)
	check(otherRepo, 1)
	if err := store.PurgeOrganization(ctx); err != nil {
		t.Fatal(err)
	}
	check(first, 0)
	check(second, 0)
	check(otherRepo, 1)
	if err := store.PurgeOrganization(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.PurgeOrganization(otherCtx); err != nil {
		t.Fatal(err)
	}
	check(otherRepo, 0)
}
