package edge

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

// addCollaboratorHandlers serves the grants that give one repository to someone who
// is not a member of the organization that owns it.
//
// A grant names a person by username or id, or a team by id. The username is passed
// through rather than resolved here: git-platform resolves it against Identity with
// its own credential, and doing it here would mean the edge asking Identity a
// question only an administrator may ask.
func addCollaboratorHandlers(h map[string]http.HandlerFunc, c gitv1.GitServiceClient) {
	h["listCollaborators"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.ListCollaborators(r.Context(), &gitv1.ListCollaboratorsRequest{
			Repo: chi.URLParam(r, "repo"),
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetGrants()))
		for _, g := range resp.GetGrants() {
			// Both fields are always present and one is empty, so a reader can tell
			// a person's grant from a team's without guessing from an absent key.
			out = append(out, map[string]any{
				"user_id": g.GetUser(), "team_id": g.GetTeamId(), "role": g.GetRole(),
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"collaborators": out})
	}

	h["addCollaborator"] = func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			// A username or a user id; git-platform resolves either.
			User   string `json:"user"`
			TeamID string `json:"team_id"`
			Role   string `json:"role"`
		}
		if err := decode(r, &body); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		_, err := c.AddCollaborator(r.Context(), &gitv1.AddCollaboratorRequest{
			Repo: chi.URLParam(r, "repo"),
			Grant: &gitv1.Collaborator{
				User: body.User, TeamId: body.TeamID, Role: body.Role,
			},
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusCreated, map[string]string{"status": "granted"})
	}

	h["removeCollaborator"] = func(w http.ResponseWriter, r *http.Request) {
		// The subject is in the path so a revoke is a plain DELETE. A team grant is
		// revoked by passing the team id as the subject, which the RPC tells apart
		// because a team id is only ever a UUID naming a team.
		subject := chi.URLParam(r, "subject")
		req := &gitv1.RemoveCollaboratorRequest{Repo: chi.URLParam(r, "repo")}
		if r.URL.Query().Get("kind") == "team" {
			req.TeamId = subject
		} else {
			req.User = subject
		}
		if _, err := c.RemoveCollaborator(r.Context(), req); err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
	}
}
