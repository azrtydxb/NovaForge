package graph

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/novaforge/novaforge/internal/authz"
)

// FileIndex is everything the indexer learned about one file at one commit:
// its symbols, the packages it imports, the references it makes by name, and
// — when the push that changed it is known — the commit and which of its
// symbols that commit's lines touched.
type FileIndex struct {
	OrgID  uuid.UUID
	RepoID uuid.UUID
	Path   string

	Evidence *FileEvidence

	// Symbols are the file's symbol nodes. Each carries attrs "path", "dir",
	// "name" and "kind"; Key must be unique within the organization.
	Symbols []Node
	Imports []FileImport
	// References name what the file's code uses. They are resolved against
	// symbol nodes by directory and name when either side is indexed.
	References []FileReference

	// Commit is the commit this file's change is attributed to, or nil when
	// no commit is known (the edges above are still written).
	Commit *CommitInfo
	// Changed holds the keys of the symbols whose lines the change touched;
	// only those gain a changed_by edge to Commit.
	Changed map[string]bool
}

// FileImport is one package a file imports. Dir is the package's directory
// within the repository, or "" for a package from outside it.
type FileImport struct {
	ImportPath string
	Dir        string
}

// FileReference is a reference a file makes, by name. FromKey is the key of
// the referencing symbol node, or "" when the reference sits outside every
// symbol, in which case the file node is the one that depends.
type FileReference struct {
	FromKey      string
	TargetDir    string
	TargetName   string
	TargetMethod bool
	// Kind is depends_on, or tested_by for a reference made from a test.
	Kind string
}

// CommitInfo describes one commit as a changed_by edge's target.
type CommitInfo struct {
	SHA         string
	Author      string
	Message     string
	At          time.Time
	WorkItemKey string
}

// FileNodeKey is the key of repoID's node for the file at path.
func FileNodeKey(repoID uuid.UUID, path string) string {
	return repoID.String() + ":file:" + path
}

// PackageNodeKey is the key of repoID's node for the package in dir (for a
// package in the repository) or at importPath (for one outside it).
func PackageNodeKey(repoID uuid.UUID, imp FileImport) string {
	if imp.Dir != "" {
		return repoID.String() + ":pkg:" + imp.Dir
	}
	return repoID.String() + ":pkg:ext:" + imp.ImportPath
}

// CommitNodeKey is the key of repoID's node for commit sha.
func CommitNodeKey(repoID uuid.UUID, sha string) string {
	return repoID.String() + ":commit:" + sha
}

// maxHistoryPerSymbol bounds the changed_by edges one symbol carries. A
// symbol's most recent changes are what a reader and an agent's context use;
// an unbounded history would grow with every push forever.
const maxHistoryPerSymbol = 20

// ReplaceFileIndex replaces everything the graph holds for fi.Path with fi,
// in one transaction, and re-derives the dependency and test edges between
// this file and every other file in the repository.
//
// Replacing a file's symbols deletes their nodes, and a deleted node takes
// every edge into it along with it — including edges from files this push
// did not touch. Those edges are therefore never stored only as edges: each
// file's references are kept by name in file_references, and edges in both
// directions are re-derived from them, so re-indexing either end of a
// relation leaves exactly the relations the current sources make. The
// changed_by history of a symbol is carried across the replacement by name,
// because the commits that changed it are still the commits that changed it.
func (s *Store) ReplaceFileIndex(ctx context.Context, fi FileIndex) error {
	if err := authz.RequireOrg(ctx, fi.OrgID); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	history, err := symbolHistory(ctx, tx, fi.OrgID, fi.RepoID, fi.Path)
	if err != nil {
		return err
	}
	if err := clearFile(ctx, tx, fi.OrgID, fi.RepoID, fi.Path); err != nil {
		return err
	}

	attrs := map[string]string{"path": fi.Path, "dir": dirOf(fi.Path)}
	if fi.Evidence != nil {
		attrs["source_hash"] = fi.Evidence.ContentHash
		attrs["module_hash"] = fi.Evidence.ModuleHash
		if fi.Evidence.Complete {
			attrs["parse_complete"] = "true"
		}
	}
	fileNode, err := upsertNode(ctx, tx, fi.OrgID, fi.RepoID, true, Node{
		OrgID: fi.OrgID, Kind: "file", Key: FileNodeKey(fi.RepoID, fi.Path),
		Attrs: attrs,
	})
	if err != nil {
		return fmt.Errorf("upsert file node: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM graph.graph_edges WHERE from_id = $1 AND kind = 'depends_on'`, fileNode.ID); err != nil {
		return fmt.Errorf("clear file dependencies: %w", err)
	}

	symbolIDs := make(map[string]uuid.UUID, len(fi.Symbols))
	for _, n := range fi.Symbols {
		if n.OrgID != fi.OrgID {
			return fmt.Errorf("symbol %s org %s does not match %s", n.Key, n.OrgID, fi.OrgID)
		}
		n.Kind = "symbol"
		saved, err := upsertNode(ctx, tx, fi.OrgID, fi.RepoID, true, n)
		if err != nil {
			return fmt.Errorf("upsert symbol %s: %w", n.Key, err)
		}
		symbolIDs[n.Key] = saved.ID
		for _, commitID := range history[n.Attrs["kind"]+"|"+n.Attrs["name"]] {
			if err := upsertEdge(ctx, tx, Edge{FromID: saved.ID, ToID: commitID, Kind: "changed_by"}); err != nil {
				return fmt.Errorf("carry history of %s: %w", n.Key, err)
			}
		}
	}

	for _, imp := range fi.Imports {
		attrs := map[string]string{"import_path": imp.ImportPath}
		if imp.Dir != "" {
			attrs["dir"] = imp.Dir
		} else {
			attrs["external"] = "true"
		}
		pkg, err := upsertNode(ctx, tx, fi.OrgID, fi.RepoID, true, Node{
			OrgID: fi.OrgID, Kind: "package", Key: PackageNodeKey(fi.RepoID, imp), Attrs: attrs,
		})
		if err != nil {
			return fmt.Errorf("upsert package %s: %w", imp.ImportPath, err)
		}
		if err := upsertEdge(ctx, tx, Edge{FromID: fileNode.ID, ToID: pkg.ID, Kind: "depends_on"}); err != nil {
			return fmt.Errorf("file imports %s: %w", imp.ImportPath, err)
		}
	}

	fileKey := FileNodeKey(fi.RepoID, fi.Path)
	for _, r := range fi.References {
		fromKey, fromKind := r.FromKey, "symbol"
		if fromKey == "" {
			fromKey, fromKind = fileKey, "file"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO graph.file_references
				(org_id, repo_id, from_path, from_key, from_kind, target_dir, target_name, target_method, edge_kind)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`, fi.OrgID, fi.RepoID, fi.Path, fromKey, fromKind, r.TargetDir, r.TargetName, r.TargetMethod, r.Kind); err != nil {
			return fmt.Errorf("record reference to %s.%s: %w", r.TargetDir, r.TargetName, err)
		}
	}

	if err := deriveEdges(ctx, tx, fi.OrgID, fi.RepoID, fi.Path); err != nil {
		return err
	}

	if fi.Commit != nil {
		commit, err := upsertNode(ctx, tx, fi.OrgID, fi.RepoID, true, Node{
			OrgID: fi.OrgID, Kind: "commit", Key: CommitNodeKey(fi.RepoID, fi.Commit.SHA),
			Attrs: map[string]string{
				"sha":           fi.Commit.SHA,
				"author":        fi.Commit.Author,
				"message":       fi.Commit.Message,
				"changed_at":    fi.Commit.At.UTC().Format(time.RFC3339),
				"work_item_key": fi.Commit.WorkItemKey,
			},
		})
		if err != nil {
			return fmt.Errorf("upsert commit %s: %w", fi.Commit.SHA, err)
		}
		if err := upsertEdge(ctx, tx, Edge{FromID: fileNode.ID, ToID: commit.ID, Kind: "changed_by"}); err != nil {
			return fmt.Errorf("file changed by %s: %w", fi.Commit.SHA, err)
		}
		for key := range fi.Changed {
			id, ok := symbolIDs[key]
			if !ok {
				continue
			}
			if err := upsertEdge(ctx, tx, Edge{FromID: id, ToID: commit.ID, Kind: "changed_by"}); err != nil {
				return fmt.Errorf("symbol changed by %s: %w", fi.Commit.SHA, err)
			}
		}
	}

	if err := pruneHistory(ctx, tx, fi.OrgID, fi.RepoID, fi.Path); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// RemoveFileIndex removes everything the graph holds for a file that no
// longer exists: its symbols, its references, and its file node, with every
// edge into or out of them.
func (s *Store) RemoveFileIndex(ctx context.Context, orgID, repoID uuid.UUID, path string) error {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := clearFile(ctx, tx, orgID, repoID, path); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM graph.graph_nodes WHERE org_id = $1 AND kind = 'file' AND key = $2`,
		orgID, FileNodeKey(repoID, path)); err != nil {
		return fmt.Errorf("delete file node: %w", err)
	}
	return tx.Commit(ctx)
}

func clearFile(ctx context.Context, tx pgx.Tx, orgID, repoID uuid.UUID, path string) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM graph.graph_nodes
		WHERE org_id = $1 AND repo_id = $2 AND kind = 'symbol' AND attrs->>'path' = $3
	`, orgID, repoID, path); err != nil {
		return fmt.Errorf("delete stale symbols: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM graph.file_references WHERE org_id = $1 AND repo_id = $2 AND from_path = $3
	`, orgID, repoID, path); err != nil {
		return fmt.Errorf("delete stale references: %w", err)
	}
	return nil
}

// symbolHistory reads the changed_by targets of path's current symbols,
// keyed by "kind|name" — the identity a symbol keeps when its lines move.
func symbolHistory(ctx context.Context, tx pgx.Tx, orgID, repoID uuid.UUID, path string) (map[string][]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `
		SELECT s.attrs->>'kind', s.attrs->>'name', e.to_id
		FROM graph.graph_nodes s
		JOIN graph.graph_edges e ON e.from_id = s.id AND e.kind = 'changed_by'
		WHERE s.org_id = $1 AND s.repo_id = $2 AND s.kind = 'symbol' AND s.attrs->>'path' = $3
	`, orgID, repoID, path)
	if err != nil {
		return nil, fmt.Errorf("read symbol history: %w", err)
	}
	defer rows.Close()
	out := map[string][]uuid.UUID{}
	for rows.Next() {
		var kind, name *string
		var to uuid.UUID
		if err := rows.Scan(&kind, &name, &to); err != nil {
			return nil, fmt.Errorf("scan symbol history: %w", err)
		}
		if kind == nil || name == nil {
			continue
		}
		key := *kind + "|" + *name
		out[key] = append(out[key], to)
	}
	return out, rows.Err()
}

// deriveEdges writes every dependency and test edge that has path at either
// end: references made from path to symbols anywhere in the repository, and
// references made from anywhere to symbols in path. A reference resolves to
// every non-test symbol of its name in its target directory, methods only for
// method calls. A test file's symbols are never a target — a test depends on
// code, code does not depend on its tests.
func deriveEdges(ctx context.Context, tx pgx.Tx, orgID, repoID uuid.UUID, path string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO graph.graph_edges (from_id, to_id, kind)
		SELECT DISTINCT
			CASE WHEN r.edge_kind = 'depends_on' THEN src.id ELSE tgt.id END,
			CASE WHEN r.edge_kind = 'depends_on' THEN tgt.id ELSE src.id END,
			r.edge_kind
		FROM graph.file_references r
		JOIN graph.graph_nodes src
			ON src.org_id = r.org_id AND src.kind = r.from_kind AND src.key = r.from_key
		JOIN graph.graph_nodes tgt
			ON tgt.org_id = r.org_id AND tgt.repo_id = r.repo_id AND tgt.kind = 'symbol'
			AND tgt.attrs->>'dir' = r.target_dir
			AND tgt.attrs->>'name' = r.target_name
			AND ((tgt.attrs->>'kind') = 'method') = r.target_method
			AND right(tgt.attrs->>'path', 8) <> '_test.go'
		WHERE r.org_id = $1 AND r.repo_id = $2
		  AND (r.from_path = $3 OR tgt.attrs->>'path' = $3)
		  AND src.id <> tgt.id
		ON CONFLICT (from_id, to_id, kind) DO NOTHING
	`, orgID, repoID, path)
	if err != nil {
		return fmt.Errorf("derive edges for %s: %w", path, err)
	}
	return nil
}

// pruneHistory keeps only the most recent maxHistoryPerSymbol changed_by
// edges of each of path's symbols.
func pruneHistory(ctx context.Context, tx pgx.Tx, orgID, repoID uuid.UUID, path string) error {
	_, err := tx.Exec(ctx, `
		DELETE FROM graph.graph_edges e
		USING (
			SELECT e2.from_id, e2.to_id,
				row_number() OVER (PARTITION BY e2.from_id ORDER BY c.attrs->>'changed_at' DESC) AS rn
			FROM graph.graph_edges e2
			JOIN graph.graph_nodes s ON s.id = e2.from_id
			JOIN graph.graph_nodes c ON c.id = e2.to_id
			WHERE e2.kind = 'changed_by' AND s.org_id = $1 AND s.repo_id = $2
			  AND s.kind = 'symbol' AND s.attrs->>'path' = $3
		) ranked
		WHERE e.kind = 'changed_by' AND e.from_id = ranked.from_id AND e.to_id = ranked.to_id AND ranked.rn > $4
	`, orgID, repoID, path, maxHistoryPerSymbol)
	if err != nil {
		return fmt.Errorf("prune history of %s: %w", path, err)
	}
	return nil
}

// dirOf is path's directory in the form the indexer uses: "" for the
// repository root, never ".".
func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return ""
}

// FileRelations is what the graph knows about one file.
type FileRelations struct {
	Symbols    []Node
	Imports    []Node
	ImportedBy []Node
	Dependents []Node
	Tests      []Node
	History    []Node
}

// ErrFileNotIndexed reports a file the graph holds nothing for.
var ErrFileNotIndexed = errors.New("file is not indexed")

// FileRelationsFor answers, for the file at path, what it imports, which
// files import its package, which code outside it depends on its symbols,
// which tests cover them, and the commits that changed it.
func (s *Store) FileRelationsFor(ctx context.Context, orgID, repoID uuid.UUID, path string) (FileRelations, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return FileRelations{}, err
	}
	var out FileRelations
	var err error

	out.Symbols, err = s.queryNodes(ctx, `
		SELECT id, org_id, kind, key, attrs FROM graph.graph_nodes
		WHERE org_id = $1 AND repo_id = $2 AND kind = 'symbol' AND attrs->>'path' = $3
		ORDER BY (attrs->>'start_line')::int NULLS LAST
	`, orgID, repoID, path)
	if err != nil {
		return FileRelations{}, err
	}

	fileKey := FileNodeKey(repoID, path)
	out.Imports, err = s.queryNodes(ctx, `
		SELECT n.id, n.org_id, n.kind, n.key, n.attrs
		FROM graph.graph_nodes f
		JOIN graph.graph_edges e ON e.from_id = f.id AND e.kind = 'depends_on'
		JOIN graph.graph_nodes n ON n.id = e.to_id AND n.org_id = $1
		WHERE f.org_id = $1 AND f.kind = 'file' AND f.key = $2 AND n.kind = 'package'
		ORDER BY n.attrs->>'import_path'
	`, orgID, fileKey)
	if err != nil {
		return FileRelations{}, err
	}
	if len(out.Symbols) == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM graph.graph_nodes WHERE org_id = $1 AND kind = 'file' AND key = $2)`,
			orgID, fileKey).Scan(&exists); err != nil {
			return FileRelations{}, fmt.Errorf("look up file node: %w", err)
		}
		if !exists {
			return FileRelations{}, ErrFileNotIndexed
		}
	}

	out.ImportedBy, err = s.queryNodes(ctx, `
		SELECT DISTINCT n.id, n.org_id, n.kind, n.key, n.attrs
		FROM graph.graph_nodes p
		JOIN graph.graph_edges e ON e.to_id = p.id AND e.kind = 'depends_on'
		JOIN graph.graph_nodes n ON n.id = e.from_id AND n.org_id = $1 AND n.kind = 'file'
		WHERE p.org_id = $1 AND p.kind = 'package' AND p.key = $2 AND n.attrs->>'path' <> $3
	`, orgID, PackageNodeKey(repoID, FileImport{Dir: dirOrRoot(path)}), path)
	if err != nil {
		return FileRelations{}, err
	}

	out.Dependents, err = s.queryNodes(ctx, `
		SELECT DISTINCT n.id, n.org_id, n.kind, n.key, n.attrs
		FROM graph.graph_nodes t
		JOIN graph.graph_edges e ON e.to_id = t.id AND e.kind = 'depends_on'
		JOIN graph.graph_nodes n ON n.id = e.from_id AND n.org_id = $1
		WHERE t.org_id = $1 AND t.repo_id = $2 AND t.kind = 'symbol' AND t.attrs->>'path' = $3
		  AND n.attrs->>'path' <> $3
	`, orgID, repoID, path)
	if err != nil {
		return FileRelations{}, err
	}

	out.Tests, err = s.queryNodes(ctx, `
		SELECT DISTINCT n.id, n.org_id, n.kind, n.key, n.attrs
		FROM graph.graph_nodes t
		JOIN graph.graph_edges e ON e.from_id = t.id AND e.kind = 'tested_by'
		JOIN graph.graph_nodes n ON n.id = e.to_id AND n.org_id = $1
		WHERE t.org_id = $1 AND t.repo_id = $2 AND t.kind = 'symbol' AND t.attrs->>'path' = $3
	`, orgID, repoID, path)
	if err != nil {
		return FileRelations{}, err
	}

	out.History, err = s.queryNodes(ctx, `
		SELECT n.id, n.org_id, n.kind, n.key, n.attrs
		FROM graph.graph_nodes f
		JOIN graph.graph_edges e ON e.from_id = f.id AND e.kind = 'changed_by'
		JOIN graph.graph_nodes n ON n.id = e.to_id AND n.org_id = $1
		WHERE f.org_id = $1 AND f.kind = 'file' AND f.key = $2
		ORDER BY n.attrs->>'changed_at' DESC
		LIMIT 50
	`, orgID, fileKey)
	if err != nil {
		return FileRelations{}, err
	}
	return out, nil
}

// dirOrRoot is dirOf, except that a file at the repository root belongs to
// the package whose node the indexer keys by "." (a package node's key needs
// a non-empty directory to be told apart from an external import).
func dirOrRoot(path string) string {
	if d := dirOf(path); d != "" {
		return d
	}
	return "."
}

func (s *Store) queryNodes(ctx context.Context, sql string, args ...any) ([]Node, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query nodes: %w", err)
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("scan node: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SortHistory orders commit (or work_item) nodes most recent first by their
// changed_at attribute, which is RFC 3339 in UTC and so sorts as text.
func SortHistory(nodes []Node) {
	sort.SliceStable(nodes, func(i, j int) bool {
		return nodes[i].Attrs["changed_at"] > nodes[j].Attrs["changed_at"]
	})
}
