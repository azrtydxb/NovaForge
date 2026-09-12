package edge

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
)

// addWriteHandlers mounts the operations that change something and had no
// route: commenting on a Work Item, assigning one, creating a branch, and
// running CI on demand.
//
// Each was already implemented in a service and reachable only by an agent
// through MCP or a typed tool. A platform whose own interface cannot do what
// it lets an agent do is one a person has to leave to get work done.
func addWriteHandlers(
	h map[string]http.HandlerFunc,
	g gitv1.GitServiceClient,
	w workv1.WorkServiceClient,
	ci civ1.CIServiceClient,
) {
	repoID := func(r *http.Request) (string, error) {
		resp, err := g.GetRepo(r.Context(), &gitv1.GetRepoRequest{Name: chi.URLParam(r, "repo")})
		if err != nil {
			return "", err
		}
		return resp.GetRepo().GetId(), nil
	}

	// workItemID resolves the API's human-readable key to the id the work
	// service keys on, so a URL stays readable.
	workItemID := func(r *http.Request) (string, error) {
		resp, err := w.GetItem(r.Context(), &workv1.GetItemRequest{Key: chi.URLParam(r, "key")})
		if err != nil {
			return "", err
		}
		return resp.GetItem().GetId(), nil
	}

	if w != nil {
		h["listWorkComments"] = func(wr http.ResponseWriter, r *http.Request) {
			id, err := workItemID(r)
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			resp, err := w.ListComments(r.Context(), &workv1.ListCommentsRequest{WorkItemId: id})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			out := make([]map[string]any, 0, len(resp.GetComments()))
			for _, c := range resp.GetComments() {
				out = append(out, commentJSON(c))
			}
			WriteJSON(wr, http.StatusOK, map[string]any{"comments": out})
		}

		h["addWorkComment"] = func(wr http.ResponseWriter, r *http.Request) {
			var req struct {
				Body string `json:"body"`
			}
			if err := decode(r, &req); err != nil {
				WriteError(wr, http.StatusBadRequest, err)
				return
			}
			id, err := workItemID(r)
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			resp, err := w.AddComment(r.Context(), &workv1.AddCommentRequest{
				WorkItemId: id, Body: req.Body,
			})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			WriteJSON(wr, http.StatusCreated, commentJSON(resp.GetComment()))
		}

		h["assignWorkItem"] = func(wr http.ResponseWriter, r *http.Request) {
			var req struct {
				AssigneeID   string `json:"assignee_id"`
				AssigneeKind string `json:"assignee_kind"`
			}
			if err := decode(r, &req); err != nil {
				WriteError(wr, http.StatusBadRequest, err)
				return
			}
			id, err := workItemID(r)
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			resp, err := w.AssignItem(r.Context(), &workv1.AssignItemRequest{
				Id: id, AssigneeId: req.AssigneeID, AssigneeKind: req.AssigneeKind,
			})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			WriteJSON(wr, http.StatusOK, WorkItemJSON(resp.GetItem()))
		}
	}

	h["createBranch"] = func(wr http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
			From string `json:"from"`
		}
		if err := decode(r, &req); err != nil {
			WriteError(wr, http.StatusBadRequest, err)
			return
		}
		resp, err := g.CreateBranch(r.Context(), &gitv1.CreateBranchRequest{
			Repo: chi.URLParam(r, "repo"), Name: req.Name, FromRef: req.From,
		})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusCreated, map[string]any{
			"name": resp.GetRef().GetName(),
			"sha":  resp.GetRef().GetSha(),
			"kind": resp.GetRef().GetKind(),
		})
	}

	if ci != nil {
		h["triggerCIRun"] = func(wr http.ResponseWriter, r *http.Request) {
			var req struct {
				Ref string `json:"ref"`
			}
			// A trigger with no body means the repository's default branch.
			_ = decode(r, &req)
			rid, err := repoID(r)
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			resp, err := ci.TriggerRun(r.Context(), &civ1.TriggerRunRequest{
				RepoId: rid, Ref: req.Ref,
			})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			WriteJSON(wr, http.StatusCreated, map[string]any{
				"id":         resp.GetRun().GetId(),
				"ref":        resp.GetRun().GetRef(),
				"commit_sha": resp.GetRun().GetCommitSha(),
				"status":     resp.GetRun().GetStatus(),
			})
		}
	}
}

func commentJSON(c *workv1.Comment) map[string]any {
	return map[string]any{
		"id":          c.GetId(),
		"author_id":   c.GetAuthorId(),
		"author_kind": c.GetAuthorKind(),
		"body":        c.GetBody(),
		"created_at":  c.GetCreatedAt(),
	}
}
