package semanticindex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func fixtureSnapshot() Snapshot {
	return Snapshot{OrgID: uuid.New(), RepoID: uuid.New(), Revision: strings.Repeat("a", 40), RootURI: "file:///workspace", ExecutionDigest: digest([]byte("unit-test context")), Files: map[string][]byte{"a.ts": []byte("const a = 1;\n"), "b.ts": []byte("const b = a;\n")}}
}
func fixtureEvidence(t *testing.T, s Snapshot) RunEvidence {
	t.Helper()
	d, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return RunEvidence{Revision: s.Revision, SnapshotDigest: d, ExecutionDigest: s.ExecutionDigest, Tool: "fixture", ToolVersion: "1"}
}
func scipJSON(t *testing.T, docs []any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"metadata": map[string]any{"project_root": "file:///workspace", "tool_info": map[string]any{"name": "fixture", "version": "1"}}, "documents": docs})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestSCIPExactIdentityAndLocalScope(t *testing.T) {
	s := fixtureSnapshot()
	input := scipJSON(t, []any{
		map[string]any{"relative_path": "a.ts", "occurrences": []any{map[string]any{"range": []int{0, 6, 7}, "symbol": "local 0", "symbol_roles": 1}}},
		map[string]any{"relative_path": "b.ts", "occurrences": []any{map[string]any{"range": []int{0, 6, 7}, "symbol": "local 0", "symbol_roles": 1}, map[string]any{"range": []int{0, 10, 11}, "symbol": "local 0"}}},
	})
	result, err := ImportSCIP(context.Background(), s, fixtureEvidence(t, s), strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	a, b := result.Documents[0], result.Documents[1]
	if a.Occurrences[0].SymbolID == b.Occurrences[0].SymbolID {
		t.Fatal("document-local IDs collided")
	}
	if b.Occurrences[0].SymbolID != b.Occurrences[1].SymbolID {
		t.Fatal("reference lost exact target identity")
	}
	if result.AbsenceSafe {
		t.Fatal("tool success cannot prove absence of dynamic references")
	}
	if a.ContentHash == "" || result.Revision != s.Revision || result.ArtifactHash == "" {
		t.Fatal("missing revision-bound evidence")
	}
}
func TestSCIPRejectsStaleAndHostileEvidence(t *testing.T) {
	s := fixtureSnapshot()
	e := fixtureEvidence(t, s)
	for _, tc := range []struct {
		name   string
		mutate func(*Snapshot, *RunEvidence)
		path   string
	}{
		{"stale revision", func(s *Snapshot, e *RunEvidence) { e.Revision = strings.Repeat("b", 40) }, "a.ts"},
		{"changed execution context", func(s *Snapshot, e *RunEvidence) { s.ExecutionDigest = digest([]byte("different GOOS or build tags")) }, "a.ts"},
		{"missing execution evidence", func(s *Snapshot, e *RunEvidence) { e.ExecutionDigest = "" }, "a.ts"},
		{"changed source", func(s *Snapshot, e *RunEvidence) { s.Files["a.ts"] = []byte("changed\n") }, "a.ts"},
		{"traversal", func(*Snapshot, *RunEvidence) {}, "../a.ts"},
		{"unknown source", func(*Snapshot, *RunEvidence) {}, "missing.ts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := fixtureSnapshot()
			e = fixtureEvidence(t, local)
			tc.mutate(&local, &e)
			_, err := ImportSCIP(context.Background(), local, e, strings.NewReader(scipJSON(t, []any{map[string]any{"relative_path": tc.path}})))
			if err == nil {
				t.Fatal("accepted invalid evidence")
			}
		})
	}
}
func TestSCIPMissingAndDiagnosticsRemainPartial(t *testing.T) {
	s := fixtureSnapshot()
	input := scipJSON(t, []any{map[string]any{"relative_path": "a.ts", "occurrences": []any{map[string]any{"range": []int{0, 0, 1}, "diagnostics": []any{map[string]any{"severity": 1, "message": "unresolved name"}}}}}})
	result, err := ImportSCIP(context.Background(), s, fixtureEvidence(t, s), strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.MissingPaths) != 1 || result.MissingPaths[0] != "b.ts" || result.Documents[0].Status != "diagnostics" {
		t.Fatalf("dishonest coverage: %+v", result)
	}
}
func TestSnapshotPreservesLiteralPaths(t *testing.T) {
	s := fixtureSnapshot()
	s.Files = map[string][]byte{" space ü\n.ts ": []byte("x\n")}
	if _, err := s.Digest(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", "/a.ts", "../a.ts", "a/../b.ts", "a//b.ts", "a\x00b.ts"} {
		s.Files = map[string][]byte{p: nil}
		if _, err := s.Digest(); err == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	s.Files = map[string][]byte{"a.ts": nil}
	for _, root := range []string{"file:///workspace?", "file:///workspace/../other", "file://remote/workspace", "https://example.invalid/workspace"} {
		s.RootURI = root
		if _, err := s.Digest(); err == nil {
			t.Fatalf("accepted noncanonical workspace root %q", root)
		}
	}
}
