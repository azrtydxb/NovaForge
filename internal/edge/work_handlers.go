package edge

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
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
			WriteJSON(wr, http.StatusCreated, WorkItemJSON(resp.GetItem()))
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
				out = append(out, WorkItemJSON(it))
			}
			WriteJSON(wr, http.StatusOK, map[string]any{"items": out})
		}

		h["getWorkItem"] = func(wr http.ResponseWriter, r *http.Request) {
			resp, err := w.GetItem(r.Context(), &workv1.GetItemRequest{Key: chi.URLParam(r, "key")})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			body := WorkItemJSON(resp.GetItem())
			// Only the single-item read knows whether a proposal awaits
			// approval; a list would have to report false for every proposal,
			// which is a claim the platform has not checked.
			body["awaiting_approval"] = resp.GetItem().GetAwaitingApproval()
			WriteJSON(wr, http.StatusOK, body)
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
				out = append(out, WorkItemJSON(it))
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
				out = append(out, ProposalJSON(p))
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
				body := WorkItemJSON(st.GetItem())
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
				// WorkItem is the Work Item's key. It used to be decoded here
				// and never passed on, so a run opened for a Work Item was
				// connected to nothing.
				WorkItem string `json:"work_item"`
			}
			if err := decode(r, &req); err != nil {
				WriteError(wr, http.StatusBadRequest, err)
				return
			}
			repo, err := g.GetRepo(r.Context(), &gitv1.GetRepoRequest{Name: chi.URLParam(r, "repo")})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			rid := repo.GetRepo().GetId()

			// The target defaults to the repository's default branch: that is
			// what a run merges into unless someone says otherwise.
			target := strings.TrimSpace(req.TargetRef)
			if target == "" {
				target = repo.GetRepo().GetDefaultBranch()
			}

			// A source branch that does not exist can never be reviewed or
			// merged. The target is not checked: a new repository's default
			// branch has no commit yet, and is still the right place to merge.
			source := strings.TrimPrefix(strings.TrimSpace(req.SourceRef), "refs/heads/")
			if source != "" {
				branches, err := g.ListBranches(r.Context(), &gitv1.ListBranchesRequest{Repo: chi.URLParam(r, "repo")})
				if err != nil {
					WriteError(wr, StatusFromGRPC(err), err)
					return
				}
				if !hasBranch(branches.GetRefs(), source) {
					WriteError(wr, http.StatusBadRequest, fmt.Errorf("source branch %q does not exist in this repository", source))
					return
				}
			}

			var workItemID string
			if key := strings.TrimSpace(req.WorkItem); key != "" {
				if w == nil {
					WriteError(wr, http.StatusNotImplemented, errors.New("this deployment has no work service to resolve a Work Item"))
					return
				}
				item, err := w.GetItem(r.Context(), &workv1.GetItemRequest{Key: key})
				if err != nil {
					WriteError(wr, StatusFromGRPC(err), err)
					return
				}
				if item.GetItem().GetRepoId() != rid {
					WriteError(wr, http.StatusBadRequest, fmt.Errorf("work item %s belongs to a different repository", key))
					return
				}
				workItemID = item.GetItem().GetId()
			}

			// The author is not sent: reviews attributes a person's run to the
			// person in the caller's scope.
			resp, err := rv.CreateRun(r.Context(), &reviewsv1.CreateRunRequest{
				RepoId: rid, Title: req.Title, WorkItemId: workItemID,
				SourceRef: source, TargetRef: target,
			})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			WriteJSON(wr, http.StatusCreated, RunJSON(resp.GetRun()))
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
				out = append(out, RunJSON(rn))
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
					WriteJSON(wr, http.StatusOK, RunJSON(rn))
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

		h["getRunPlan"] = func(wr http.ResponseWriter, r *http.Request) {
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
			resp, err := rv.ListPlan(r.Context(), &reviewsv1.ListPlanRequest{
				RepoId: rid, Number: int32(n),
			})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			out := make([]map[string]any, 0, len(resp.GetSteps()))
			for _, s := range resp.GetSteps() {
				out = append(out, map[string]any{
					"ordinal": s.GetOrdinal(),
					"text":    s.GetText(),
					"state":   s.GetState(),
				})
			}
			WriteJSON(wr, http.StatusOK, map[string]any{"steps": out})
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

// addMaintenanceDecisionHandlers mounts a person's decision on a maintenance
// proposal. It needs the agents service as well as work: approving a proposal
// for an agent names that agent, and the work service — which may not read
// the agents schema — cannot tell whether the id belongs to this
// organization's agent or to anything at all.
func addMaintenanceDecisionHandlers(h map[string]http.HandlerFunc, g gitv1.GitServiceClient, w workv1.WorkServiceClient, a agentsv1.AgentServiceClient) {
	if w == nil {
		return
	}
	repoID := func(r *http.Request) (string, error) {
		resp, err := g.GetRepo(r.Context(), &gitv1.GetRepoRequest{Name: chi.URLParam(r, "repo")})
		if err != nil {
			return "", err
		}
		return resp.GetRepo().GetId(), nil
	}

	h["approveMaintenanceProposal"] = func(wr http.ResponseWriter, r *http.Request) {
		var req struct {
			// AgentID assigns the approved Work Item to this agent. Empty,
			// the approving person takes it.
			AgentID string `json:"agent_id"`
		}
		// An approval with no body is an approval for oneself.
		if r.ContentLength != 0 {
			if err := decode(r, &req); err != nil {
				WriteError(wr, http.StatusBadRequest, err)
				return
			}
		}
		rid, err := repoID(r)
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		approve := &workv1.ApproveMaintenanceProposalRequest{
			RepoId: rid, Fingerprint: chi.URLParam(r, "fingerprint"),
		}
		if req.AgentID != "" {
			if a == nil {
				WriteError(wr, http.StatusNotImplemented, errors.New("this deployment has no agent runtime to assign an agent from"))
				return
			}
			// ListAgents is scoped to the caller's organization, so finding
			// the id there is what proves it is this organization's agent.
			agents, err := a.ListAgents(r.Context(), &agentsv1.ListAgentsRequest{})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			if !hasAgent(agents.GetAgents(), req.AgentID) {
				WriteError(wr, http.StatusBadRequest, fmt.Errorf("agent %q is not an agent of this organization", req.AgentID))
				return
			}
			approve.AssigneeId, approve.AssigneeKind = req.AgentID, "agent"
		}
		resp, err := w.ApproveMaintenanceProposal(r.Context(), approve)
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusOK, ProposalJSON(resp.GetProposal()))
	}

	h["scanRepository"] = func(wr http.ResponseWriter, r *http.Request) {
		rid, err := repoID(r)
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		resp, err := w.ScanRepository(r.Context(), &workv1.ScanRepositoryRequest{RepoId: rid})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		keys := resp.GetProposedWorkItemKeys()
		if keys == nil {
			keys = []string{}
		}
		errs := resp.GetScannerErrors()
		if errs == nil {
			errs = []string{}
		}
		WriteJSON(wr, http.StatusOK, map[string]any{
			"findings": resp.GetFindings(), "proposed_work_item_keys": keys, "scanner_errors": errs,
		})
	}

	h["dismissMaintenanceProposal"] = func(wr http.ResponseWriter, r *http.Request) {
		var req struct {
			Reason string `json:"reason"`
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
		resp, err := w.DismissMaintenanceProposal(r.Context(), &workv1.DismissMaintenanceProposalRequest{
			RepoId: rid, Fingerprint: chi.URLParam(r, "fingerprint"), Reason: req.Reason,
		})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusOK, ProposalJSON(resp.GetProposal()))
	}
}

// hasAgent reports whether agents includes id.
func hasAgent(agents []*agentsv1.Agent, id string) bool {
	for _, ag := range agents {
		if ag.GetId() == id {
			return true
		}
	}
	return false
}

// ProposalJSON renders a maintenance proposal, including the decision a
// person made on it and who holds its Work Item.
func ProposalJSON(p *workv1.MaintenanceProposal) map[string]any {
	return map[string]any{
		"fingerprint":    p.GetFingerprint(),
		"work_item_key":  p.GetWorkItemKey(),
		"work_item_goal": p.GetWorkItemGoal(),
		"work_item_type": p.GetWorkItemType(),
		"state":          p.GetState(),
		"resolved":       p.GetResolved(),
		"decision":       p.GetDecision(),
		"decided_by":     p.GetDecidedBy(),
		"decided_at":     p.GetDecidedAt(),
		"dismiss_reason": p.GetDismissReason(),
		"assignee_id":    p.GetAssigneeId(),
		"assignee_kind":  p.GetAssigneeKind(),
	}
}

// WorkItemJSON renders a Work Item for the API. The identity and assignment
// fields are carried as well as the descriptive ones: a client listing items
// needs something stable to key them by, something to order them by, and a way
// to say who holds one — without those it has to invent all three.
func WorkItemJSON(it *workv1.WorkItem) map[string]any {
	return map[string]any{
		"id": it.GetId(), "key": it.GetKey(), "type": it.GetType(),
		"goal": it.GetGoal(), "state": it.GetState(),
		"acceptance": it.GetAcceptance(), "constraints": it.GetConstraints(),
		"required_gates": it.GetRequiredGates(),
		"repo_id":        it.GetRepoId(),
		"assignee_id":    it.GetAssigneeId(), "assignee_kind": it.GetAssigneeKind(),
		"created_at": it.GetCreatedAt(),
	}
}

// RunJSON renders an Engineering Run for the API. Like a Work Item it carries
// its identity as well as its description: a run is addressed by number in the
// API, but a client still needs a stable key, the kind of author it had, and
// when it was opened.
func RunJSON(r *reviewsv1.Run) map[string]any {
	return map[string]any{
		"id": r.GetId(), "number": r.GetNumber(), "title": r.GetTitle(),
		"state": r.GetState(), "source_ref": r.GetSourceRef(),
		"target_ref": r.GetTargetRef(), "agent_name": r.GetAgentName(),
		"model_name": r.GetModelName(),
		"author_id":  r.GetAuthorId(), "author_kind": r.GetAuthorKind(),
		"work_item_id": r.GetWorkItemId(), "created_at": r.GetCreatedAt(),
	}
}

// hasBranch reports whether refs names branch.
func hasBranch(refs []*gitv1.Ref, branch string) bool {
	for _, ref := range refs {
		if ref.GetName() == branch {
			return true
		}
	}
	return false
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
