package graph

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MaintenanceSnapshot validates file identity and reads references in ONE SQL
// snapshot. Checking a checkpoint and then querying nodes separately could mix
// two revisions while the indexer replaces files. Matching every Go file's
// content and module digest also detects missed pushes and legacy/partial data.
// Resolve by-name references here too: materialized edges can lag when files
// are replaced concurrently, but each file's references and evidence are atomic.
func (s *GRPCServer) MaintenanceSnapshot(ctx context.Context, req *graphv1.MaintenanceSnapshotRequest) (*graphv1.MaintenanceSnapshotResponse, error) {
	org, err := callerOrg(ctx)
	if err != nil || org == uuid.Nil {
		return nil, status.Error(codes.PermissionDenied, "organization scope required")
	}
	repo, err := parseRepoID(req.GetRepoId())
	if err != nil {
		return nil, err
	}
	files := req.GetGoFileHashes()
	if len(files) == 0 || len(files) > 5000 || !validDigest(req.GetModuleHash()) {
		return nil, status.Error(codes.InvalidArgument, "require 1..5000 Go files and a module digest")
	}
	for name, digest := range files {
		if !strings.HasSuffix(name, ".go") || path.Clean(name) != name || path.IsAbs(name) || strings.HasPrefix(name, "../") || !validDigest(digest) {
			return nil, status.Error(codes.InvalidArgument, "invalid source manifest")
		}
	}
	rows, err := s.Store.pool.Query(ctx, `
		SELECT n.id, n.org_id, n.kind, n.key, n.attrs,
		  EXISTS (
		    SELECT 1 FROM graph.graph_edges e
		    JOIN graph.graph_nodes caller ON caller.id=e.from_id AND caller.org_id=$1 AND caller.repo_id=$2
		    WHERE e.to_id=n.id AND e.kind IN ('depends_on','called_by')
		  ) OR EXISTS (
		    SELECT 1 FROM graph.graph_edges e
		    JOIN graph.graph_nodes test ON test.id=e.to_id AND test.org_id=$1 AND test.repo_id=$2
		    WHERE e.from_id=n.id AND e.kind='tested_by'
		  ) OR EXISTS (
		    SELECT 1 FROM graph.file_references r
		    JOIN graph.graph_nodes f ON f.org_id=r.org_id AND f.repo_id=r.repo_id
		      AND f.kind='file' AND f.attrs->>'path'=r.from_path
		    WHERE r.org_id=$1 AND r.repo_id=$2
		      AND r.target_dir=COALESCE(n.attrs->>'dir','') AND r.target_name=n.attrs->>'name'
		      AND (NOT r.target_method OR n.attrs->>'kind'='method')
		      AND r.edge_kind IN ('depends_on','tested_by')
		  )
		FROM graph.graph_nodes n
		WHERE n.org_id=$1 AND n.repo_id=$2 AND n.kind IN ('file','symbol')
		  AND right(n.attrs->>'path',3)='.go'
		ORDER BY n.kind,n.key LIMIT 10001`, org, repo)
	if err != nil {
		return nil, status.Error(codes.Internal, "read graph snapshot failed")
	}
	defer rows.Close()
	out := &graphv1.MaintenanceSnapshotResponse{}
	seen := map[string]bool{}
	count := 0
	for rows.Next() {
		count++
		if count > 10000 {
			return nil, status.Error(codes.ResourceExhausted, "maintenance graph exceeds 10000 nodes")
		}
		var n Node
		var attrs []byte
		var referenced bool
		if err := rows.Scan(&n.ID, &n.OrgID, &n.Kind, &n.Key, &attrs, &referenced); err != nil {
			return nil, status.Error(codes.Internal, "decode graph snapshot failed")
		}
		if err := json.Unmarshal(attrs, &n.Attrs); err != nil {
			return nil, status.Error(codes.Internal, "decode graph attributes failed")
		}
		name := n.Attrs["path"]
		if files[name] == "" {
			return nil, status.Error(codes.FailedPrecondition, "Go graph contains removed source")
		}
		if n.Kind == "file" {
			if files[name] == "" || n.Attrs["source_hash"] != files[name] || n.Attrs["module_hash"] != req.GetModuleHash() || n.Attrs["parse_complete"] != "true" {
				return nil, status.Error(codes.FailedPrecondition, "Go graph is stale, incomplete or unavailable for this manifest")
			}
			seen[name] = true
			continue
		}
		out.Symbols = append(out.Symbols, toProtoNode(n))
		if !referenced && deadCodeCandidate(n) {
			out.Unreferenced = append(out.Unreferenced, toProtoNode(n))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, status.Error(codes.Internal, "read graph snapshot failed")
	}
	if len(seen) != len(files) {
		return nil, status.Error(codes.FailedPrecondition, "Go graph is missing source files")
	}
	return out, nil
}

func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// Exported APIs, methods, init/main and test entry points are not justified
// candidates merely because this repository has no static caller for them.
func deadCodeCandidate(n Node) bool {
	name, file := n.Attrs["name"], n.Attrs["path"]
	r, _ := utf8.DecodeRuneInString(name)
	return n.Attrs["kind"] == "function" && name != "" && name != "init" && name != "main" && !unicode.IsUpper(r) &&
		!strings.HasSuffix(file, "_test.go") && !strings.Contains("/"+file, "/vendor/") && !strings.Contains("/"+file, "/testdata/")
}
