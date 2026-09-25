package edge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"google.golang.org/grpc"
)

type guiGit struct{ gitv1.GitServiceClient }

func (guiGit) GetRepo(context.Context, *gitv1.GetRepoRequest, ...grpc.CallOption) (*gitv1.GetRepoResponse, error) {
	return &gitv1.GetRepoResponse{Repo: &gitv1.Repo{Id: "repo"}}, nil
}

type guiWork struct {
	workv1.WorkServiceClient
	called bool
}

func (*guiWork) GetItem(context.Context, *workv1.GetItemRequest, ...grpc.CallOption) (*workv1.GetItemResponse, error) {
	return &workv1.GetItemResponse{Item: &workv1.WorkItem{Id: "item", RepoId: "other-repo"}}, nil
}
func (g *guiWork) PatchItem(context.Context, *workv1.PatchItemRequest, ...grpc.CallOption) (*workv1.PatchItemResponse, error) {
	g.called = true
	return &workv1.PatchItemResponse{}, nil
}

type guiAgents struct{ agentsv1.AgentServiceClient }

func TestGUIWorkRejectsMismatchedRepositoryBeforeMutation(t *testing.T) {
	work := &guiWork{}
	handlers := map[string]http.HandlerFunc{}
	AddGUIHandlers(handlers, guiGit{}, work, nil, guiAgents{})
	h := handlers["patchWorkItem"]
	if h == nil {
		t.Fatal("PATCH workflow has no handler")
	}
	router := chi.NewRouter()
	router.Patch("/orgs/{org}/repos/{repo}/work/{key}", h)
	req := httptest.NewRequest("PATCH", "/orgs/org/repos/repo/work/NF-1", strings.NewReader(`{"goal":"changed","expected":{"type":"feature","goal":"old"}}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || work.called {
		t.Fatalf("cross-repository PATCH code=%d called=%v body=%s", rec.Code, work.called, rec.Body.String())
	}
}
