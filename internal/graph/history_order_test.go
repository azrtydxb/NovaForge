package graph_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/graph"
)

// TestSortHistoryBreaksSameSecondTiesByIndexOrder pins "last changed by" for
// two commits in one second. Git records commit times in whole seconds, so
// two commits an agent makes back to back tie on changed_at, and the sort
// then kept whatever order the database returned: CI named NF-7 as the last
// change of a symbol NF-9 had just changed.
func TestSortHistoryBreaksSameSecondTiesByIndexOrder(t *testing.T) {
	older := graph.Node{Key: "c1", Attrs: map[string]string{"changed_at": "2026-10-21T11:29:38Z", "indexed_at": "2026-10-21T11:29:38.100000000Z", "work_item_key": "NF-7"}}
	newer := graph.Node{Key: "c2", Attrs: map[string]string{"changed_at": "2026-10-21T11:29:38Z", "indexed_at": "2026-10-21T11:29:38.900000000Z", "work_item_key": "NF-9"}}
	nodes := []graph.Node{older, newer}
	graph.SortHistory(nodes)
	if nodes[0].Attrs["work_item_key"] != "NF-9" {
		t.Fatalf("latest = %s, want NF-9: of two commits in one second the one indexed later is the later change", nodes[0].Attrs["work_item_key"])
	}
}

// TestReindexKeepsFirstIndexedAt pins what the tie-break relies on: indexing a
// commit again (a refresh, a replayed push) must not move it ahead of commits
// indexed after it the first time.
func TestReindexKeepsFirstIndexedAt(t *testing.T) {
	store := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org, uuid.New())
	n := graph.Node{OrgID: org, Kind: "commit", Key: "commit:" + uuid.NewString(), Attrs: map[string]string{"indexed_at": "2026-10-21T11:29:38.100000000Z"}}
	if _, err := store.UpsertNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	n.Attrs = map[string]string{"indexed_at": "2026-10-21T12:00:00.000000000Z", "sha": "abc"}
	saved, err := store.UpsertNode(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.Attrs["indexed_at"]; got != "2026-10-21T11:29:38.100000000Z" {
		t.Fatalf("indexed_at after re-index = %q, want the first one kept", got)
	}
	if saved.Attrs["sha"] != "abc" {
		t.Fatalf("other attributes were not updated: %v", saved.Attrs)
	}
}
