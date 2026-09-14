package edge

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
)

// addAgentHandlers mounts the agent surface. Without these an agent could
// only be created, and a run only started, over gRPC from inside the
// cluster — so the one thing this platform exists to do could not be driven
// by the CLI or by anything outside.
func addAgentHandlers(h map[string]http.HandlerFunc, g gitv1.GitServiceClient, a agentsv1.AgentServiceClient, id identityv1.IdentityServiceClient) {
	if a == nil {
		return
	}

	h["createAgent"] = func(wr http.ResponseWriter, r *http.Request) {
		var req struct {
			Name     string `json:"name"`
			Role     string `json:"role"`
			ModelRef string `json:"model_ref"`
			Enabled  *bool  `json:"enabled"`
		}
		if err := decode(r, &req); err != nil {
			WriteError(wr, http.StatusBadRequest, err)
			return
		}
		// An agent is enabled unless the caller says otherwise: creating one
		// that cannot be scheduled is the unusual intent, not the default.
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		resp, err := a.CreateAgent(r.Context(), &agentsv1.CreateAgentRequest{
			Name: req.Name, Role: req.Role, ModelRef: req.ModelRef, Enabled: enabled,
		})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusCreated, agentJSON(resp.GetAgent()))
	}

	h["listAgents"] = func(wr http.ResponseWriter, r *http.Request) {
		resp, err := a.ListAgents(r.Context(), &agentsv1.ListAgentsRequest{})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetAgents()))
		for _, ag := range resp.GetAgents() {
			out = append(out, agentJSON(ag))
		}
		WriteJSON(wr, http.StatusOK, map[string]any{"agents": out})
	}

	h["startAgentRun"] = func(wr http.ResponseWriter, r *http.Request) {
		var req struct {
			AgentID               string `json:"agent_id"`
			WorkItemKey           string `json:"work_item_key"`
			SponsorID             string `json:"sponsor_id"`
			WallclockLimitSeconds int64  `json:"wallclock_limit_seconds"`
			TokenLimit            int64  `json:"token_limit"`
			CostLimitMicros       int64  `json:"cost_limit_micros"`
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
		// The sponsor defaults to the caller: someone starting a run is
		// answerable for it unless they name another member who is.
		sponsor := req.SponsorID
		if sponsor == "" {
			sponsor = resolveCaller(r, id)
		}
		if sponsor == "" {
			WriteError(wr, http.StatusBadRequest,
				errNoSponsor)
			return
		}
		resp, err := a.StartRun(r.Context(), &agentsv1.StartRunRequest{
			AgentId:               req.AgentID,
			RepoId:                repo.GetRepo().GetId(),
			WorkItemKey:           req.WorkItemKey,
			SponsorId:             sponsor,
			WallclockLimitSeconds: req.WallclockLimitSeconds,
			TokenLimit:            req.TokenLimit,
			CostLimitMicros:       req.CostLimitMicros,
		})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusCreated, agentRunJSON(resp.GetRun()))
	}

	h["agentStats"] = func(wr http.ResponseWriter, r *http.Request) {
		resp, err := a.ListStats(r.Context(), &agentsv1.ListStatsRequest{})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetStats()))
		for _, s := range resp.GetStats() {
			out = append(out, map[string]any{
				"agent_id":    s.GetAgentId(),
				"runs":        s.GetRuns(),
				"succeeded":   s.GetSucceeded(),
				"failed":      s.GetFailed(),
				"over_budget": s.GetOverBudget(),
				"running":     s.GetRunning(),
				"tokens_used": s.GetTokensUsed(),
				"token_limit": s.GetTokenLimit(),
			})
		}
		WriteJSON(wr, http.StatusOK, map[string]any{"stats": out})
	}

	h["getAgentRun"] = func(wr http.ResponseWriter, r *http.Request) {
		resp, err := a.GetRun(r.Context(), &agentsv1.GetRunRequest{Id: chi.URLParam(r, "id")})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusOK, agentRunJSON(resp.GetRun()))
	}

	h["cancelAgentRun"] = func(wr http.ResponseWriter, r *http.Request) {
		if _, err := a.CancelRun(r.Context(), &agentsv1.CancelRunRequest{Id: chi.URLParam(r, "id")}); err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusOK, map[string]any{"cancelled": chi.URLParam(r, "id")})
	}
}

// addWorkItemRunHandlers mounts the Agent Runs started against one Work Item.
// It needs both services — the key resolves through work, the runs live in
// agent-runtime — and without it nothing outside the cluster could see a run
// still going, so nothing could offer to cancel one.
func addWorkItemRunHandlers(h map[string]http.HandlerFunc, w workv1.WorkServiceClient, a agentsv1.AgentServiceClient) {
	if w == nil || a == nil {
		return
	}

	h["listWorkItemAgentRuns"] = func(wr http.ResponseWriter, r *http.Request) {
		item, err := w.GetItem(r.Context(), &workv1.GetItemRequest{Key: chi.URLParam(r, "key")})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		resp, err := a.ListRunsForWorkItem(r.Context(),
			&agentsv1.ListRunsForWorkItemRequest{WorkItemId: item.GetItem().GetId()})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetRuns()))
		for _, run := range resp.GetRuns() {
			out = append(out, agentRunJSON(run))
		}
		WriteJSON(wr, http.StatusOK, map[string]any{"runs": out})
	}
}

func agentJSON(a *agentsv1.Agent) map[string]any {
	return map[string]any{
		"id":        a.GetId(),
		"org_id":    a.GetOrgId(),
		"name":      a.GetName(),
		"role":      a.GetRole(),
		"model_ref": a.GetModelRef(),
		"enabled":   a.GetEnabled(),
	}
}

func agentRunJSON(r *agentsv1.Run) map[string]any {
	return map[string]any{
		"id":           r.GetId(),
		"agent_id":     r.GetAgentId(),
		"work_item_id": r.GetWorkItemId(),
		"sponsor_id":   r.GetSponsorId(),
		"branch":       r.GetBranch(),
		"state":        r.GetState(),
		"started_at":   r.GetStartedAt(),
		"ended_at":     r.GetEndedAt(),
	}
}

// errNoSponsor is returned when neither the request nor the caller's own
// credential yields a human to answer for the run. An agent run always has
// one; refusing here is the same invariant agent-runtime enforces, said
// earlier and more legibly.
var errNoSponsor = errors.New("an agent run needs a sponsor: name one in sponsor_id, or call with a credential that resolves to a user")

// resolveCaller returns the user id behind the request's own credential, so
// the ordinary case — a person starting a run they are answerable for —
// needs no sponsor_id at all. It returns "" when the credential does not
// resolve to a person, and the caller refuses rather than inventing one.
func resolveCaller(r *http.Request, id identityv1.IdentityServiceClient) string {
	if id == nil {
		return ""
	}
	token := CredentialFrom(r.Context())
	if token == "" {
		return ""
	}
	org := OrgRefFrom(r.Context())
	if resp, err := id.ResolveToken(r.Context(), &identityv1.ResolveTokenRequest{Token: token, Org: org}); err == nil {
		if s := resp.GetSubject(); s.GetActorKind() == "user" {
			return s.GetUserId()
		}
		return ""
	}
	resp, err := id.ResolveSession(r.Context(), &identityv1.ResolveSessionRequest{Token: token, Org: org})
	if err != nil {
		return ""
	}
	if s := resp.GetSubject(); s.GetActorKind() == "user" {
		return s.GetUserId()
	}
	return ""
}

// addRunToolHandlers mounts the tool-call history of the agent run behind an
// Engineering Run. It needs both services: a run names a Work Item, and the
// agent runs — with their audited tool calls — are keyed on that Work Item.
// Neither service can answer this alone, which is why the edge joins them.
func addRunToolHandlers(
	h map[string]http.HandlerFunc,
	g gitv1.GitServiceClient,
	rv reviewsv1.ReviewsServiceClient,
	a agentsv1.AgentServiceClient,
) {
	if rv == nil || a == nil {
		return
	}

	h["getRunToolCalls"] = func(wr http.ResponseWriter, r *http.Request) {
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
		run, err := rv.GetRun(r.Context(), &reviewsv1.GetRunRequest{
			RepoId: repo.GetRepo().GetId(), Number: int32(n),
		})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		workItemID := run.GetRun().GetWorkItemId()
		if workItemID == "" {
			// A run opened by a person carries no Work Item and therefore no
			// agent run; saying so beats an empty list that reads as "the
			// agent did nothing".
			WriteJSON(wr, http.StatusOK, map[string]any{
				"calls": []map[string]any{},
				"note":  "this run carries no Work Item, so no agent run is associated with it",
			})
			return
		}

		runs, err := a.ListRunsForWorkItem(r.Context(),
			&agentsv1.ListRunsForWorkItemRequest{WorkItemId: workItemID})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}

		// Every agent run against the Work Item contributed to this change,
		// so all of their calls are shown, newest run first.
		out := make([]map[string]any, 0)
		for _, id := range runs.GetRunIds() {
			calls, err := a.ListToolCalls(r.Context(), &agentsv1.ListToolCallsRequest{RunId: id})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			for _, c := range calls.GetCalls() {
				out = append(out, map[string]any{
					"tool":       c.GetTool(),
					"args":       c.GetArgs(),
					"outcome":    c.GetOutcome(),
					"error":      c.GetError(),
					"started_at": c.GetStartedAt(),
				})
			}
		}
		WriteJSON(wr, http.StatusOK, map[string]any{"calls": out})
	}
}
