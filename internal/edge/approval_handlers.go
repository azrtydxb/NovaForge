package edge

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
)

var errBadDecision = errors.New(`decision must be "approved" or "denied"`)

// addApprovalHandlers mounts the approvals a change raises: the organization's
// inbox of pending requests, a run's approval history, and deciding one.
//
// A request is shown with the run it belongs to — repository, number, title
// and who authored it — because a person deciding "change the database
// schema" needs to know which change, and the gates service holds only the
// run's id.
func addApprovalHandlers(h map[string]http.HandlerFunc, g gitv1.GitServiceClient, rv reviewsv1.ReviewsServiceClient, gates gatesv1.GatesServiceClient) {
	h["listApprovals"] = func(wr http.ResponseWriter, r *http.Request) {
		resp, err := gates.ListApprovals(r.Context(), &gatesv1.ListApprovalsRequest{})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		repos, err := g.ListRepos(r.Context(), &gitv1.ListReposRequest{})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		names := map[string]string{}
		for _, repo := range repos.GetRepos() {
			names[repo.GetId()] = repo.GetName()
		}
		out := make([]map[string]any, 0, len(resp.GetRequests()))
		for _, req := range resp.GetRequests() {
			item := ApprovalJSON(req)
			if run, err := rv.GetRun(r.Context(), &reviewsv1.GetRunRequest{Id: req.GetRunId()}); err == nil {
				item["run"] = map[string]any{
					"number":      run.GetRun().GetNumber(),
					"title":       run.GetRun().GetTitle(),
					"repo":        names[run.GetRun().GetRepoId()],
					"state":       run.GetRun().GetState(),
					"author_kind": run.GetRun().GetAuthorKind(),
					"agent_name":  run.GetRun().GetAgentName(),
					"source_ref":  run.GetRun().GetSourceRef(),
				}
			}
			out = append(out, item)
		}
		WriteJSON(wr, http.StatusOK, map[string]any{"approvals": out, "can_decide": resp.GetCanDecide(), "viewer_id": resp.GetViewerId()})
	}

	h["listRunApprovals"] = func(wr http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(chi.URLParam(r, "number"))
		if err != nil {
			WriteError(wr, http.StatusBadRequest, err)
			return
		}
		repo, err := g.GetRepo(r.Context(), &gitv1.GetRepoRequest{Name: chi.URLParam(r, "repo")})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		runID, err := runIDByNumber(r, rv, repo.GetRepo().GetId(), n)
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		resp, err := gates.ListApprovals(r.Context(), &gatesv1.ListApprovalsRequest{RunId: runID})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetRequests()))
		for _, req := range resp.GetRequests() {
			out = append(out, ApprovalJSON(req))
		}
		WriteJSON(wr, http.StatusOK, map[string]any{"approvals": out, "can_decide": resp.GetCanDecide(), "viewer_id": resp.GetViewerId()})
	}

	h["decideApproval"] = func(wr http.ResponseWriter, r *http.Request) {
		var body struct {
			Decision string `json:"decision"`
			Comment  string `json:"comment"`
		}
		if err := decode(r, &body); err != nil {
			WriteError(wr, http.StatusBadRequest, err)
			return
		}
		if body.Decision != "approved" && body.Decision != "denied" {
			WriteError(wr, http.StatusBadRequest, errBadDecision)
			return
		}
		// The decider is whoever this request authenticates as; the gates
		// service takes it from the forwarded credential and refuses an
		// author, an agent, or anyone who is not an owner or admin.
		resp, err := gates.ResolveApproval(r.Context(), &gatesv1.ResolveApprovalRequest{
			Id: chi.URLParam(r, "id"), Decision: body.Decision, Comment: body.Comment,
		})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusOK, ApprovalJSON(resp.GetRequest()))
	}
}

// ApprovalJSON renders one approval request. paths is always a list.
func ApprovalJSON(req *gatesv1.ApprovalRequestMsg) map[string]any {
	paths := req.GetPaths()
	if paths == nil {
		paths = []string{}
	}
	return map[string]any{
		"id":          req.GetId(),
		"run_id":      req.GetRunId(),
		"action":      req.GetAction(),
		"action_name": humanActionName(req.GetAction()),
		"decision":    req.GetDecision(),
		"reason":      req.GetReason(),
		"paths":       paths,
		"head_sha":    req.GetHeadSha(),
		"comment":     req.GetComment(),
		"author_id":   req.GetAuthorId(),
		"author_kind": req.GetAuthorKind(),
		"decided_by":  req.GetDecidedBy(),
		"decided_at":  req.GetDecidedAt(),
		"created_at":  req.GetCreatedAt(),
	}
}
