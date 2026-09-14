package edge_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/edge"
)

// These doubles stand in for the services behind the edge: what is under test
// is the edge's own translation of a REST request into RPCs, which is where
// a field silently goes missing.

type gitDouble struct {
	gitv1.GitServiceClient
	repos    map[string]*gitv1.Repo
	branches []string
}

func (g *gitDouble) GetRepo(_ context.Context, in *gitv1.GetRepoRequest, _ ...grpc.CallOption) (*gitv1.GetRepoResponse, error) {
	if r, ok := g.repos[in.GetName()]; ok {
		return &gitv1.GetRepoResponse{Repo: r}, nil
	}
	return nil, status.Error(codes.NotFound, "no such repository")
}

func (g *gitDouble) ListBranches(_ context.Context, _ *gitv1.ListBranchesRequest, _ ...grpc.CallOption) (*gitv1.ListBranchesResponse, error) {
	out := make([]*gitv1.Ref, 0, len(g.branches))
	for _, b := range g.branches {
		out = append(out, &gitv1.Ref{Name: b, Kind: "branch"})
	}
	return &gitv1.ListBranchesResponse{Refs: out}, nil
}

type workDouble struct {
	workv1.WorkServiceClient
	items map[string]*workv1.WorkItem
}

func (w *workDouble) GetItem(_ context.Context, in *workv1.GetItemRequest, _ ...grpc.CallOption) (*workv1.GetItemResponse, error) {
	if it, ok := w.items[in.GetKey()]; ok {
		return &workv1.GetItemResponse{Item: it}, nil
	}
	return nil, status.Error(codes.NotFound, "no such work item")
}

type reviewsDouble struct {
	reviewsv1.ReviewsServiceClient
	created *reviewsv1.CreateRunRequest
}

func (r *reviewsDouble) CreateRun(_ context.Context, in *reviewsv1.CreateRunRequest, _ ...grpc.CallOption) (*reviewsv1.CreateRunResponse, error) {
	r.created = in
	return &reviewsv1.CreateRunResponse{Run: &reviewsv1.Run{
		Id: "run-1", Number: 1, Title: in.GetTitle(), SourceRef: in.GetSourceRef(),
		TargetRef: in.GetTargetRef(), WorkItemId: in.GetWorkItemId(), State: "open",
	}}, nil
}

// call invokes one operation's handler with the path parameters the router
// would have extracted.
func call(t *testing.T, h map[string]http.HandlerFunc, op, method, body string, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	handler, ok := h[op]
	if !ok {
		t.Fatalf("no handler for %s", op)
	}
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	rc := chi.NewRouteContext()
	for k, v := range params {
		rc.URLParams.Add(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func runDoubles() (*gitDouble, *workDouble, *reviewsDouble) {
	g := &gitDouble{
		repos:    map[string]*gitv1.Repo{"platform": {Id: "repo-1", Name: "platform", DefaultBranch: "main"}},
		branches: []string{"main", "feature/parser"},
	}
	w := &workDouble{items: map[string]*workv1.WorkItem{
		"NF-4": {Id: "item-4", Key: "NF-4", RepoId: "repo-1"},
		"NF-9": {Id: "item-9", Key: "NF-9", RepoId: "another-repo"},
	}}
	return g, w, &reviewsDouble{}
}

// TestCreateRunCarriesTheWorkItem pins a field the edge accepted and dropped:
// createRun decoded work_item from the body and never passed it on, so a run
// opened for a Work Item was connected to nothing — no tool-call history, and
// no way from the Work Item to the change made for it.
func TestCreateRunCarriesTheWorkItem(t *testing.T) {
	g, w, rv := runDoubles()
	h := edge.Handlers(edge.Config{Git: g, Work: w, Reviews: rv})

	rec := call(t, h, "createRun", http.MethodPost,
		`{"title":"tidy the parser","source_ref":"feature/parser","target_ref":"main","work_item":"NF-4"}`,
		map[string]string{"org": "acme", "repo": "platform"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if rv.created == nil || rv.created.GetWorkItemId() != "item-4" {
		t.Fatalf("CreateRun got work_item_id %q, want the resolved id item-4", rv.created.GetWorkItemId())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["work_item_id"] != "item-4" {
		t.Fatalf("response does not carry the work item: %s", rec.Body.String())
	}
}

// TestCreateRunRefusesWhatCannotBeMerged pins the checks the edge can make
// before a run exists: a Work Item from another repository, and a source
// branch that does not exist.
func TestCreateRunRefusesWhatCannotBeMerged(t *testing.T) {
	g, w, rv := runDoubles()
	h := edge.Handlers(edge.Config{Git: g, Work: w, Reviews: rv})
	params := map[string]string{"org": "acme", "repo": "platform"}

	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"work item in another repository": {`{"title":"t","source_ref":"feature/parser","target_ref":"main","work_item":"NF-9"}`, http.StatusBadRequest},
		"unknown work item":               {`{"title":"t","source_ref":"feature/parser","target_ref":"main","work_item":"NF-404"}`, http.StatusNotFound},
		"source branch does not exist":    {`{"title":"t","source_ref":"feature/gone","target_ref":"main"}`, http.StatusBadRequest},
	} {
		rv.created = nil
		rec := call(t, h, "createRun", http.MethodPost, tc.body, params)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d (%s)", name, rec.Code, tc.want, rec.Body.String())
		}
		if rv.created != nil {
			t.Errorf("%s: a run was created anyway", name)
		}
	}
}
