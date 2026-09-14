package edge

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
)

// addGateEvaluationHandlers mounts running a run's gates on request. The gate
// controller was never called by anything, so no run ever had a gate outcome:
// its Evidence stayed empty and a required gate blocked its merge forever.
// Merging now evaluates first as well; this is for seeing the outcome before
// deciding to merge.
func addGateEvaluationHandlers(h map[string]http.HandlerFunc, g gitv1.GitServiceClient, rv reviewsv1.ReviewsServiceClient, gates gatesv1.GatesServiceClient) {
	h["evaluateRunGates"] = func(wr http.ResponseWriter, r *http.Request) {
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
		resp, err := gates.Evaluate(r.Context(), &gatesv1.EvaluateRequest{RunId: runID})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetEvaluations()))
		for _, e := range resp.GetEvaluations() {
			out = append(out, map[string]any{
				"gate": e.GetGate(), "status": e.GetStatus(), "detail": e.GetDetail(),
				"target_sha": e.GetTargetSha(), "evaluated_at": e.GetEvaluatedAt(),
			})
		}
		WriteJSON(wr, http.StatusOK, map[string]any{"evaluations": out})
	}
}
