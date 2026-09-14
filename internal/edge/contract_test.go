package edge_test

import (
	"testing"

	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/edge"
)

// TestWorkItemJSONCarriesIdentity pins the fields a client cannot do without.
// The renderer originally carried only the descriptive ones, so every listing
// had no stable key, nothing to order by, and no way to say who held an item —
// the GUI invented all three and then crashed on the undefined values.
func TestWorkItemJSONCarriesIdentity(t *testing.T) {
	body := edge.WorkItemJSON(&workv1.WorkItem{
		Id: "11111111-1111-1111-1111-111111111111", Key: "NF-1", Type: "feature",
		Goal: "a goal", State: "open", RepoId: "22222222-2222-2222-2222-222222222222",
		AssigneeKind: "agent", CreatedAt: "2026-09-12T10:00:00Z",
	})
	for _, k := range []string{
		"id", "key", "type", "goal", "state",
		"repo_id", "assignee_id", "assignee_kind", "created_at",
	} {
		if _, ok := body[k]; !ok {
			t.Errorf("workItemJSON omits %q", k)
		}
	}
}

// TestSearchCodeJSONSaysHowItWasFound pins the code-search body the GUI and
// tests/e2e/search_test.sh read: every result's location and score, and the
// mode, because an empty lexical answer and an empty semantic one mean
// different things and a reader must be able to tell them apart.
func TestSearchCodeJSONSaysHowItWasFound(t *testing.T) {
	body := edge.SearchCodeJSON(&graphv1.SearchCodeResponse{
		Mode: "semantic",
		Chunks: []*graphv1.CodeChunk{
			{Path: "billing/vat.go", StartLine: 3, EndLine: 9, Text: "func VATTotal()", Score: 0.82},
		},
	})
	if body["mode"] != "semantic" {
		t.Errorf("mode = %v, want semantic", body["mode"])
	}
	results, ok := body["results"].([]map[string]any)
	if !ok || len(results) != 1 {
		t.Fatalf("results = %#v, want one result", body["results"])
	}
	for _, k := range []string{"path", "start_line", "end_line", "score", "text"} {
		if _, ok := results[0][k]; !ok {
			t.Errorf("search result omits %q", k)
		}
	}
	if results[0]["path"] != "billing/vat.go" {
		t.Errorf("path = %v", results[0]["path"])
	}

	empty := edge.SearchCodeJSON(&graphv1.SearchCodeResponse{Mode: "lexical"})
	if r, ok := empty["results"].([]map[string]any); !ok || r == nil {
		t.Errorf("an empty search must render results as [], not null: %#v", empty["results"])
	}
}

// TestRunJSONCarriesIdentity pins the same for Engineering Runs.
func TestRunJSONCarriesIdentity(t *testing.T) {
	body := edge.RunJSON(&reviewsv1.Run{
		Id: "33333333-3333-3333-3333-333333333333", Number: 7, Title: "a run",
		State: "open", SourceRef: "agents/NF-1/work", TargetRef: "main",
		AuthorKind: "agent", CreatedAt: "2026-09-12T10:00:00Z",
	})
	for _, k := range []string{
		"id", "number", "title", "state", "source_ref", "target_ref",
		"author_id", "author_kind", "work_item_id", "created_at",
	} {
		if _, ok := body[k]; !ok {
			t.Errorf("runJSON omits %q", k)
		}
	}
}
