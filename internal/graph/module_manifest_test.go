package graph_test

import (
	"github.com/google/uuid"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	"github.com/novaforge/novaforge/internal/graph"
	"testing"
)

func TestMaintenanceModuleManifestExactIdentity(t *testing.T) {
	s := newStore(t)
	org, repo := uuid.New(), uuid.New()
	ctx := scopedCtx(org, uuid.New())
	hash := graph.SourceDigest(nil)
	fi := graph.FileIndex{OrgID: org, RepoID: repo, Path: "nested/code.go", Evidence: &graph.FileEvidence{ContentHash: hash, ModuleHash: hash, ModulePath: "nested/go.mod", Complete: true}}
	if err := s.ReplaceFileIndex(ctx, fi); err != nil {
		t.Fatal(err)
	}
	srv := graph.NewGRPCServer(s, nil, nil, nil, nil, nil)
	request := func() *graphv1.MaintenanceSnapshotRequest {
		return &graphv1.MaintenanceSnapshotRequest{RepoId: repo.String(), ModuleHash: hash, GoFileHashes: map[string]string{fi.Path: hash}, GoFileModuleHashes: map[string]string{fi.Path: hash}, GoFileModulePaths: map[string]string{fi.Path: "nested/go.mod"}}
	}
	if _, err := srv.MaintenanceSnapshot(ctx, request()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*graphv1.MaintenanceSnapshotRequest)
	}{
		{"missing paths", func(r *graphv1.MaintenanceSnapshotRequest) { r.GoFileModulePaths = nil }},
		{"missing hashes", func(r *graphv1.MaintenanceSnapshotRequest) { r.GoFileModuleHashes = nil }},
		{"extra key", func(r *graphv1.MaintenanceSnapshotRequest) { r.GoFileModulePaths["other.go"] = "" }},
		{"relocated identical module", func(r *graphv1.MaintenanceSnapshotRequest) { r.GoFileModulePaths[fi.Path] = "go.mod" }},
		{"non ancestor", func(r *graphv1.MaintenanceSnapshotRequest) { r.GoFileModulePaths[fi.Path] = "other/go.mod" }},
		{"explicit absence differs", func(r *graphv1.MaintenanceSnapshotRequest) { r.GoFileModulePaths[fi.Path] = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request()
			tc.mutate(r)
			if _, err := srv.MaintenanceSnapshot(ctx, r); err == nil {
				t.Fatal("accepted mismatched module evidence")
			}
		})
	}
	fi.Evidence.ModulePath = ""
	if err := s.ReplaceFileIndex(ctx, fi); err != nil {
		t.Fatal(err)
	}
	r := request()
	r.GoFileModulePaths[fi.Path] = ""
	if _, err := srv.MaintenanceSnapshot(ctx, r); err != nil {
		t.Fatalf("known absence: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `UPDATE graph.graph_nodes SET attrs=attrs-'module_path' WHERE org_id=$1 AND repo_id=$2 AND kind='file'`, org, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.MaintenanceSnapshot(ctx, r); err == nil {
		t.Fatal("legacy missing attribute treated as known absence")
	}
}
