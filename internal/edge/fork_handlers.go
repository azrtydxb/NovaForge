package edge

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

// addForkHandlers wires forking a repository.
//
// There is no "list forks" route: every repository the caller can see already
// reports the repository it was forked from, so a client that has the
// repository list can name a repository's forks without another request — and
// asking git-platform for "the forks of this repository" would be a second way
// to answer the same question, which is how two answers start disagreeing.
func addForkHandlers(h map[string]http.HandlerFunc, c gitv1.GitServiceClient) {
	h["forkRepo"] = func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
			// ToOrg is accepted and forwarded rather than silently ignored, so a
			// caller asking for a fork in another organization is told no instead
			// of getting one in their own and believing it went where they asked.
			ToOrg string `json:"to_org"`
		}
		if err := decode(r, &body); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := c.ForkRepo(r.Context(), &gitv1.ForkRepoRequest{
			Repo: chi.URLParam(r, "repo"), Name: body.Name, ToOrg: body.ToOrg,
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusCreated, repoJSON(resp.GetRepo()))
	}
}

// sourceRepoName resolves the name of the repository a run's source ref lives
// in, for a run whose source is a fork. It returns "" for a run whose source is
// its own repository, and also when the lookup fails: the name is a label on a
// screen, and a run must still be readable when the repository it was proposed
// from has since been renamed or deleted.
func sourceRepoName(ctx context.Context, c gitv1.GitServiceClient, repoID, sourceRepoID string) string {
	if c == nil || sourceRepoID == "" || sourceRepoID == repoID {
		return ""
	}
	resp, err := c.GetRepo(ctx, &gitv1.GetRepoRequest{Name: sourceRepoID})
	if err != nil {
		return ""
	}
	return resp.GetRepo().GetName()
}
