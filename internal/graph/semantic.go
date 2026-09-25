package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/semanticindex"
)

// ReplaceSemanticIndex publishes a whole-project result atomically on the exact
// index lock session. It cannot be called as an unfenced late producer callback.
// Positive compiler identities replace heuristic edges for covered source;
// absence is never claimed, including for an entirely successful tool run.
func (s *Store) ReplaceSemanticIndex(ctx context.Context, p semanticindex.Produced) error {
	snap := p.Snapshot
	if err := authz.RequireOrg(ctx, snap.OrgID); err != nil {
		return err
	}
	session, ok := ctx.Value(indexSessionKey{}).(*indexSession)
	if !ok || session.conn == nil || session.org != snap.OrgID || session.repo != snap.RepoID {
		return fmt.Errorf("semantic publication requires the producing index session")
	}
	snapshotDigest, err := snap.Digest()
	if err != nil {
		return err
	}
	if SourceDigest(p.Manifest) != snap.ExecutionDigest || !json.Valid(p.Manifest) {
		return fmt.Errorf("semantic execution manifest mismatch")
	}
	hashes := map[string]string{}
	for name, source := range snap.Files {
		hashes[name] = SourceDigest(source)
	}
	covered := map[string]bool{}
	for _, r := range p.Results {
		if r.OrgID != snap.OrgID || r.RepoID != snap.RepoID || r.Revision != snap.Revision || r.SnapshotDigest != snapshotDigest || r.ExecutionDigest != snap.ExecutionDigest || r.AbsenceSafe || r.Tool == "" || r.ToolVersion == "" || !validDigest(r.ArtifactHash) {
			return fmt.Errorf("semantic result identity mismatch")
		}
		for _, d := range r.Documents {
			if hashes[d.Path] == "" || hashes[d.Path] != d.ContentHash {
				return fmt.Errorf("semantic document source mismatch")
			}
			covered[d.Path] = true
		}
	}
	if p.Definitions != nil {
		d := p.Definitions
		if d.OrgID != snap.OrgID || d.RepoID != snap.RepoID || d.Revision != snap.Revision || d.SnapshotDigest != snapshotDigest || d.ExecutionDigest != snap.ExecutionDigest || d.AbsenceSafe {
			return fmt.Errorf("LSP result identity mismatch")
		}
		for _, r := range d.Resolutions {
			if _, ok := hashes[r.Query.Path]; !ok {
				return fmt.Errorf("LSP query outside snapshot")
			}
			for _, target := range r.Targets {
				if hashes[target.Path] != target.ContentHash {
					return fmt.Errorf("LSP target source mismatch")
				}
			}
		}
	}
	sourceManifest, err := json.Marshal(hashes)
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(struct {
		SCIP []semanticindex.Result
		LSP  *semanticindex.DefinitionResult
	}{p.Results, p.Definitions})
	if err != nil {
		return err
	}
	if len(evidence) > semanticindex.MaxArtifactBytes {
		return fmt.Errorf("semantic stored evidence exceeds limit")
	}
	tx, err := beginIndexWrite(ctx, s.pool, snap.OrgID, snap.RepoID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Remove the old generation even when the new project contains no supported
	// language. Per-file syntax refresh must never leave old semantic edges alive.
	if _, err = tx.Exec(ctx, `DELETE FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND attrs->>'semantic'='true'`, snap.OrgID, snap.RepoID); err != nil {
		return err
	}
	fileIDs := map[string]uuid.UUID{}
	for path := range covered {
		// Clearing all references for a compiler-covered file prevents by-name
		// fallback from recreating ambiguous method edges on the next file refresh.
		if _, err = tx.Exec(ctx, `DELETE FROM graph.file_references WHERE org_id=$1 AND repo_id=$2 AND from_path=$3`, snap.OrgID, snap.RepoID, path); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM graph.graph_edges e USING graph.graph_nodes n WHERE n.org_id=$1 AND n.repo_id=$2 AND n.attrs->>'path'=$3 AND ((e.from_id=n.id AND e.kind='depends_on') OR (e.to_id=n.id AND e.kind='tested_by'))`, snap.OrgID, snap.RepoID, path); err != nil {
			return err
		}
		n, err := upsertNode(ctx, tx, snap.OrgID, snap.RepoID, true, Node{OrgID: snap.OrgID, Kind: "file", Key: FileNodeKey(snap.RepoID, path), Attrs: map[string]string{"path": path, "dir": dirOf(path), "source_hash": hashes[path], "semantic_revision": snap.Revision, "absence_safe": "false"}})
		if err != nil {
			return err
		}
		fileIDs[path] = n.ID
	}
	definitions := map[string][]uuid.UUID{}
	locations := map[string]uuid.UUID{}
	for _, r := range p.Results {
		for _, d := range r.Documents {
			for _, o := range d.Occurrences {
				if !o.Definition {
					continue
				}
				// Keep full exact identity and range. Display names come from the actual
				// source span; they never participate in reference resolution.
				name, err := semanticSpan(snap.Files[d.Path], o.Range, d.PositionEncoding)
				if err != nil {
					return err
				}
				if name == "" {
					continue
				}
				key := snap.RepoID.String() + ":" + o.SymbolID + ":" + d.Path + ":" + strconv.Itoa(o.Range.Start.Line) + ":" + strconv.Itoa(o.Range.Start.Character)
				n, err := upsertNode(ctx, tx, snap.OrgID, snap.RepoID, true, Node{OrgID: snap.OrgID, Kind: "symbol", Key: key, Attrs: map[string]string{"path": d.Path, "dir": dirOf(d.Path), "name": name, "kind": "semantic", "semantic": "true", "symbol_id": o.SymbolID, "start_line": strconv.Itoa(o.Range.Start.Line + 1), "end_line": strconv.Itoa(o.Range.End.Line + 1), "revision": snap.Revision, "absence_safe": "false"}})
				if err != nil {
					return err
				}
				// Existing changed-line attribution belongs to the same literal
				// declaration position. This does not resolve references by name.
				if _, err := tx.Exec(ctx, `INSERT INTO graph.graph_edges(from_id,to_id,kind)
                 SELECT $1,e.to_id,e.kind FROM graph.graph_edges e JOIN graph.graph_nodes old ON old.id=e.from_id
                 WHERE old.org_id=$2 AND old.repo_id=$3 AND old.kind='symbol' AND old.attrs->>'path'=$4
                 AND old.attrs->>'start_line'=$5 AND old.attrs->>'name'=$6 AND e.kind='changed_by'
                 ON CONFLICT DO NOTHING`, n.ID, snap.OrgID, snap.RepoID, d.Path, strconv.Itoa(o.Range.Start.Line+1), name); err != nil {
					return err
				}
				definitions[o.SymbolID] = append(definitions[o.SymbolID], n.ID)
				// LSP speaks UTF-16. The start position identifies a definition location,
				// not a guessed name; retain ambiguous locations as unresolved.
				pos, err := semanticUTF16Position(snap.Files[d.Path], o.Range.Start, d.PositionEncoding)
				if err != nil {
					return err
				}
				loc := semanticLocation(d.Path, pos)
				if _, exists := locations[loc]; exists {
					locations[loc] = uuid.Nil
				} else {
					locations[loc] = n.ID
				}
			}
		}
	}
	link := func(path string, target uuid.UUID) error {
		from, ok := fileIDs[path]
		if !ok || target == uuid.Nil {
			return nil
		}
		edge := Edge{FromID: from, ToID: target, Kind: "depends_on"}
		if strings.HasSuffix(path, "_test.go") || strings.Contains(path, ".test.") || strings.Contains(path, ".spec.") || strings.HasPrefix(path, "test_") || strings.Contains(path, "/test_") {
			edge = Edge{FromID: target, ToID: from, Kind: "tested_by"}
		}
		return upsertEdge(ctx, tx, edge)
	}
	for _, r := range p.Results {
		for _, d := range r.Documents {
			for _, o := range d.Occurrences {
				if !o.Definition {
					for _, target := range definitions[o.SymbolID] {
						if err := link(d.Path, target); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	if p.Definitions != nil {
		for _, r := range p.Definitions.Resolutions {
			for _, target := range r.Targets {
				if err := link(r.Query.Path, locations[semanticLocation(target.Path, target.Range.Start)]); err != nil {
					return err
				}
			}
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO graph.semantic_snapshots(org_id,repo_id,revision,snapshot_digest,execution_digest,execution_manifest,source_manifest,evidence) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(org_id,repo_id) DO UPDATE SET revision=EXCLUDED.revision,snapshot_digest=EXCLUDED.snapshot_digest,execution_digest=EXCLUDED.execution_digest,execution_manifest=EXCLUDED.execution_manifest,source_manifest=EXCLUDED.source_manifest,evidence=EXCLUDED.evidence`, snap.OrgID, snap.RepoID, snap.Revision, snapshotDigest, snap.ExecutionDigest, p.Manifest, sourceManifest, evidence)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func semanticLocation(path string, p semanticindex.Position) string {
	return fmt.Sprintf("%s:%d:%d", path, p.Line, p.Character)
}
func semanticUTF16Position(source []byte, p semanticindex.Position, encoding int) (semanticindex.Position, error) {
	lines := strings.Split(string(source), "\n")
	if p.Line < 0 || p.Line >= len(lines) {
		return p, fmt.Errorf("semantic line out of bounds")
	}
	offset, err := semanticByteOffset(lines[p.Line], p.Character, encoding)
	if err != nil {
		return p, err
	}
	p.Character = len(utf16.Encode([]rune(lines[p.Line][:offset])))
	return p, nil
}
func semanticSpan(source []byte, r semanticindex.Range, encoding int) (string, error) {
	lines := strings.Split(string(source), "\n")
	if r.Start.Line < 0 || r.Start.Line >= len(lines) || r.End.Line < r.Start.Line || r.End.Line >= len(lines) {
		return "", fmt.Errorf("semantic range out of bounds")
	}
	if r.Start.Line != r.End.Line {
		return "", nil
	}
	start, err := semanticByteOffset(lines[r.Start.Line], r.Start.Character, encoding)
	if err != nil {
		return "", err
	}
	end, err := semanticByteOffset(lines[r.End.Line], r.End.Character, encoding)
	if err != nil || end < start {
		return "", fmt.Errorf("semantic range invalid")
	}
	return lines[r.Start.Line][start:end], nil
}
func semanticByteOffset(line string, column, encoding int) (int, error) {
	// Unspecified SCIP encoding is not permission to guess UTF-8/UTF-16.
	// ASCII columns are identical under every supported encoding; non-ASCII
	// lines must remain unavailable until the producer supplies the encoding.
	if encoding == 0 {
		for _, r := range line {
			if r > 127 {
				return 0, fmt.Errorf("semantic encoding unspecified for non-ASCII source")
			}
		}
		encoding = 1
	}
	if encoding < 1 || encoding > 3 {
		return 0, fmt.Errorf("semantic encoding invalid")
	}
	units := 0
	for offset, r := range line {
		if units == column {
			return offset, nil
		}
		switch encoding {
		case 1:
			units += len(string(r))
		case 2:
			units++
			if r > 0xffff {
				units++
			}
		case 3:
			units++
		}
	}
	if units == column {
		return len(line), nil
	}
	return 0, fmt.Errorf("semantic column invalid")
}
