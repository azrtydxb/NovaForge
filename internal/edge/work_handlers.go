package edge

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
)

// addWorkHandlers mounts the Work Item and Engineering Run operations. They are
// separated from the identity and git handlers so each service's surface is
// readable on its own.
func addWorkHandlers(h map[string]http.HandlerFunc, g gitv1.GitServiceClient, w workv1.WorkServiceClient, rv reviewsv1.ReviewsServiceClient) {
	// The API names repositories; work and reviews key on repository ids. The
	// edge resolves one to the other rather than leaking uuids into URLs, which
	// would make every clone URL and CLI argument unusable by a human.
	repoID := func(r *http.Request) (string, error) {
		resp, err := g.GetRepo(r.Context(), &gitv1.GetRepoRequest{Name: chi.URLParam(r, "repo")})
		if err != nil {
			return "", err
		}
		return resp.GetRepo().GetId(), nil
	}

	if w != nil {
		h["createWorkItem"] = func(wr http.ResponseWriter, r *http.Request) {
			var req struct {
				Type          string   `json:"type"`
				Goal          string   `json:"goal"`
				Acceptance    []string `json:"acceptance"`
				Constraints   []string `json:"constraints"`
				RequiredGates []string `json:"required_gates"`
			}
			if err := decode(r, &req); err != nil {
				WriteError(wr, http.StatusBadRequest, err)
				return
			}
			rid, err := repoID(r)
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			resp, err := w.CreateItem(r.Context(), &workv1.CreateItemRequest{
				RepoId: rid, Type: req.Type, Goal: req.Goal,
				Acceptance: req.Acceptance, Constraints: req.Constraints,
				RequiredGates: req.RequiredGates,
			})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			WriteJSON(wr, http.StatusCreated, workItemJSON(resp.GetItem()))
		}

		h["listWorkItems"] = func(wr http.ResponseWriter, r *http.Request) {
			rid, err := repoID(r)
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			resp, err := w.ListItems(r.Context(), &workv1.ListItemsRequest{
				RepoId: rid, State: r.URL.Query().Get("state"),
			})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			out := make([]map[string]any, 0, len(resp.GetItems()))
			for _, it := range resp.GetItems() {
				out = append(out, workItemJSON(it))
			}
			WriteJSON(wr, http.StatusOK, map[string]any{"items": out})
		}

		h["getWorkItem"] = func(wr http.ResponseWriter, r *http.Request) {
			resp, err := w.GetItem(r.Context(), &workv1.GetItemRequest{Key: chi.URLParam(r, "key")})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			WriteJSON(wr, http.StatusOK, workItemJSON(resp.GetItem()))
		}
	}

	if w != nil {
		h["decomposeEpic"] = func(wr http.ResponseWriter, r *http.Request) {
			resp, err := w.DecomposeEpic(r.Context(), &workv1.DecomposeEpicRequest{
				EpicKey: chi.URLParam(r, "key"),
			})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			out := make([]map[string]any, 0, len(resp.GetSubtasks()))
			for _, it := range resp.GetSubtasks() {
				out = append(out, workItemJSON(it))
			}
			WriteJSON(wr, http.StatusCreated, map[string]any{"subtasks": out})
		}

		h["listMaintenanceProposals"] = func(wr http.ResponseWriter, r *http.Request) {
			rid, err := repoID(r)
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			resp, err := w.ListMaintenanceProposals(r.Context(),
				&workv1.ListMaintenanceProposalsRequest{RepoId: rid})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			out := make([]map[string]any, 0, len(resp.GetProposals()))
			for _, p := range resp.GetProposals() {
				out = append(out, map[string]any{
					"fingerprint":    p.GetFingerprint(),
					"work_item_key":  p.GetWorkItemKey(),
					"work_item_goal": p.GetWorkItemGoal(),
					"work_item_type": p.GetWorkItemType(),
					"state":          p.GetState(),
					"resolved":       p.GetResolved(),
				})
			}
			WriteJSON(wr, http.StatusOK, map[string]any{"proposals": out})
		}

		h["listSubtasks"] = func(wr http.ResponseWriter, r *http.Request) {
			resp, err := w.ListSubtasks(r.Context(), &workv1.ListSubtasksRequest{
				EpicKey: chi.URLParam(r, "key"),
			})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			out := make([]map[string]any, 0, len(resp.GetSubtasks()))
			for _, st := range resp.GetSubtasks() {
				body := workItemJSON(st.GetItem())
				body["ready"] = st.GetReady()
				out = append(out, body)
			}
			WriteJSON(wr, http.StatusOK, map[string]any{"subtasks": out})
		}
	}

	if rv != nil {
		h["getDashboard"] = func(wr http.ResponseWriter, r *http.Request) {
			resp, err := rv.GetExceptions(r.Context(), &reviewsv1.GetExceptionsRequest{})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			sm := resp.GetSummary()
			items := make([]map[string]any, 0, len(resp.GetItems()))
			for _, it := range resp.GetItems() {
				items = append(items, map[string]any{
					"key": it.GetKey(), "title": it.GetTitle(),
					"state": it.GetState(), "reason": it.GetReason(),
				})
			}
			WriteJSON(wr, http.StatusOK, map[string]any{
				"agents_running":         sm.GetAgentsRunning(),
				"ready_to_auto_merge":    sm.GetReadyToAutoMerge(),
				"need_human_review":      sm.GetNeedHumanReview(),
				"architecture_decisions": sm.GetArchitectureDecisions(),
				"gate_failures":          sm.GetGateFailures(),
				"agents_blocked":         sm.GetAgentsBlocked(),
				"exceptions":             items,
			})
		}

		h["createRun"] = func(wr http.ResponseWriter, r *http.Request) {
			var req struct {
				Title     string `json:"title"`
				SourceRef string `json:"source_ref"`
				TargetRef string `json:"target_ref"`
				WorkItem  string `json:"work_item"`
			}
			if err := decode(r, &req); err != nil {
				WriteError(wr, http.StatusBadRequest, err)
				return
			}
			rid, err := repoID(r)
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			resp, err := rv.CreateRun(r.Context(), &reviewsv1.CreateRunRequest{
				RepoId: rid, Title: req.Title, AuthorKind: "user",
				SourceRef: req.SourceRef, TargetRef: req.TargetRef,
			})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			WriteJSON(wr, http.StatusCreated, runJSON(resp.GetRun()))
		}

		h["listRuns"] = func(wr http.ResponseWriter, r *http.Request) {
			rid, err := repoID(r)
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			resp, err := rv.ListRuns(r.Context(), &reviewsv1.ListRunsRequest{RepoId: rid})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			out := make([]map[string]any, 0, len(resp.GetRuns()))
			for _, rn := range resp.GetRuns() {
				out = append(out, runJSON(rn))
			}
			WriteJSON(wr, http.StatusOK, map[string]any{"runs": out})
		}

		h["getRun"] = func(wr http.ResponseWriter, r *http.Request) {
			n, err := strconv.Atoi(chi.URLParam(r, "number"))
			if err != nil {
				WriteError(wr, http.StatusBadRequest, err)
				return
			}
			rid, err := repoID(r)
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			// Runs are addressed by number in the API and by id in the service,
			// and there is no by-number RPC, so the run is located in the list.
			list, err := rv.ListRuns(r.Context(), &reviewsv1.ListRunsRequest{RepoId: rid})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			for _, rn := range list.GetRuns() {
				if int(rn.GetNumber()) == n {
					WriteJSON(wr, http.StatusOK, runJSON(rn))
					return
				}
			}
			WriteError(wr, http.StatusNotFound, errRunNotFound(n))
		}

		// The by-number addressing now reaches the service directly: getRun
		// above still scans ListRuns because it predates ReviewsService
		// accepting (repo_id, number), which these two use.
		h["getRunProof"] = func(wr http.ResponseWriter, r *http.Request) {
			n, err := strconv.Atoi(chi.URLParam(r, "number"))
			if err != nil {
				WriteError(wr, http.StatusBadRequest, err)
				return
			}
			rid, err := repoID(r)
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			resp, err := rv.ListProof(r.Context(), &reviewsv1.ListProofRequest{RepoId: rid, Number: int32(n)})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			out := make([]map[string]any, 0, len(resp.GetProof()))
			for _, p := range resp.GetProof() {
				out = append(out, map[string]any{
					"gate":        p.GetGate(),
					"status":      p.GetStatus(),
					"detail":      p.GetDetail(),
					"recorded_at": p.GetRecordedAt(),
				})
			}
			WriteJSON(wr, http.StatusOK, map[string]any{"proof": out})
		}

		h["mergeRun"] = func(wr http.ResponseWriter, r *http.Request) {
			n, err := strconv.Atoi(chi.URLParam(r, "number"))
			if err != nil {
				WriteError(wr, http.StatusBadRequest, err)
				return
			}
			var req struct {
				Method string `json:"method"`
			}
			// A merge with no body is an ordinary merge; an absent body is
			// not an error.
			_ = decode(r, &req)
			rid, err := repoID(r)
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			resp, err := rv.MergeRun(r.Context(), &reviewsv1.MergeRunRequest{
				RepoId: rid, Number: int32(n), Method: req.Method,
			})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			WriteJSON(wr, http.StatusOK, map[string]any{"merge_sha": resp.GetMergeSha()})
		}

		h["submitReview"] = func(wr http.ResponseWriter, r *http.Request) {
			n, err := strconv.Atoi(chi.URLParam(r, "number"))
			if err != nil {
				WriteError(wr, http.StatusBadRequest, err)
				return
			}
			var req struct {
				Verdict string `json:"verdict"`
				Summary string `json:"summary"`
			}
			if err := decode(r, &req); err != nil {
				WriteError(wr, http.StatusBadRequest, err)
				return
			}
			rid, err := repoID(r)
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			runID, err := runIDByNumber(r, rv, rid, n)
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			if _, err := rv.SubmitReview(r.Context(), &reviewsv1.SubmitReviewRequest{
				RunId: runID, Verdict: req.Verdict,
			}); err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			WriteJSON(wr, http.StatusCreated, map[string]string{"status": "recorded"})
		}
	}
}

func workItemJSON(it *workv1.WorkItem) map[string]any {
	return map[string]any{
		"key": it.GetKey(), "type": it.GetType(), "goal": it.GetGoal(),
		"state": it.GetState(), "acceptance": it.GetAcceptance(),
		"constraints": it.GetConstraints(), "required_gates": it.GetRequiredGates(),
	}
}

func runJSON(r *reviewsv1.Run) map[string]any {
	return map[string]any{
		"number": r.GetNumber(), "title": r.GetTitle(), "state": r.GetState(),
		"source_ref": r.GetSourceRef(), "target_ref": r.GetTargetRef(),
		"agent_name": r.GetAgentName(), "model_name": r.GetModelName(),
	}
}

func errRunNotFound(n int) error {
	return fmt.Errorf("no run #%d in this repository", n)
}

// runIDByNumber maps the API's run number onto the service's run id.
func runIDByNumber(r *http.Request, rv reviewsv1.ReviewsServiceClient, repoID string, number int) (string, error) {
	list, err := rv.ListRuns(r.Context(), &reviewsv1.ListRunsRequest{RepoId: repoID})
	if err != nil {
		return "", err
	}
	for _, rn := range list.GetRuns() {
		if int(rn.GetNumber()) == number {
			return rn.GetId(), nil
		}
	}
	return "", errRunNotFound(number)
}
