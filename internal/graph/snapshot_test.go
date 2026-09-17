package graph_test

import (
	"testing"

	"github.com/google/uuid"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	"github.com/novaforge/novaforge/internal/graph"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMaintenanceSnapshotRequiresExactCompleteManifest(t *testing.T) {
	store := newStore(t)
	org, repo := uuid.New(), uuid.New()
	ctx := scopedCtx(org, uuid.New())
	module := graph.SourceDigest([]byte("module example.com/probe\n"))
	digest := graph.SourceDigest([]byte("package p\nfunc unused() {}\n"))
	fi := graph.FileIndex{
		OrgID: org, RepoID: repo, Path: "code.go",
		Evidence: &graph.FileEvidence{ContentHash: digest, ModuleHash: module, Complete: true},
		Symbols: []graph.Node{{OrgID: org, Key: repo.String() + ":unused", Attrs: map[string]string{
			"path": "code.go", "name": "unused", "kind": "function", "dir": "",
		}}},
	}
	if err := store.ReplaceFileIndex(ctx, fi); err != nil {
		t.Fatal(err)
	}
	srv := graph.NewGRPCServer(store, nil, nil, nil, nil, nil)
	req := &graphv1.MaintenanceSnapshotRequest{RepoId: repo.String(), GoFileHashes: map[string]string{"code.go": digest}, ModuleHash: module}
	got, err := srv.MaintenanceSnapshot(ctx, req)
	if err != nil {
		t.Fatalf("complete matching evidence: %v", err)
	}
	if len(got.GetSymbols()) != 1 || len(got.GetUnreferenced()) != 1 {
		t.Fatalf("missing graph evidence: %+v", got)
	}
	t.Run("by-name evidence survives delayed edge materialization", func(t *testing.T) {
		withRef := fi
		withRef.References = []graph.FileReference{{TargetName: "unused", Kind: "depends_on"}}
		if err := store.ReplaceFileIndex(ctx, withRef); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := store.ReplaceFileIndex(ctx, fi); err != nil {
				t.Error(err)
			}
		}()
		if _, err := store.Pool().Exec(ctx, `DELETE FROM graph.graph_edges WHERE from_id IN (SELECT id FROM graph.graph_nodes WHERE org_id=$1)`, org); err != nil {
			t.Fatal(err)
		}
		got, err := srv.MaintenanceSnapshot(ctx, req)
		if err != nil || len(got.GetUnreferenced()) != 0 {
			t.Fatalf("ignored recorded reference without materialized edge: %+v %v", got, err)
		}
	})
	for _, tc := range []struct {
		name, module string
		files        map[string]string
	}{
		{"changed source", module, map[string]string{"code.go": graph.SourceDigest([]byte("changed"))}},
		{"missing source", module, map[string]string{"code.go": digest, "new.go": digest}},
		{"deleted source", module, map[string]string{"new.go": digest}},
		{"changed module", graph.SourceDigest([]byte("other module")), map[string]string{"code.go": digest}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.MaintenanceSnapshot(ctx, &graphv1.MaintenanceSnapshotRequest{RepoId: repo.String(), GoFileHashes: tc.files, ModuleHash: tc.module})
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("mismatched evidence accepted: %v", err)
			}
		})
	}
	_, err = srv.MaintenanceSnapshot(scopedCtx(uuid.New(), uuid.New()), req)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("foreign caller saw evidence: %v", err)
	}
	fi.Evidence.Complete = false
	if err := store.ReplaceFileIndex(ctx, fi); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.MaintenanceSnapshot(ctx, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("skipped parse accepted: %v", err)
	}
	fi.Evidence = nil
	if err := store.ReplaceFileIndex(ctx, fi); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.MaintenanceSnapshot(ctx, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("legacy evidence accepted: %v", err)
	}
}
