package edge

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

// addMirrorHandlers mounts import and mirroring. It lives with the repository
// routes because both act on a repository, and it goes through git-platform for
// the same reason.
//
// No response here carries the upstream credential, and no handler logs a
// request body: the credential arrives in one, and a log line is a read like any
// other — the same rule the webhook handlers follow for a hook secret.
func addMirrorHandlers(h map[string]http.HandlerFunc, c gitv1.GitServiceClient) {
	h["importRepo"] = func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name            string `json:"name"`
			Remote          string `json:"remote"`
			Credential      string `json:"credential"`
			Mirror          bool   `json:"mirror"`
			IntervalSeconds int32  `json:"interval_seconds"`
		}
		if err := decode(r, &body); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := c.ImportRepo(r.Context(), &gitv1.ImportRepoRequest{
			Name: body.Name, Remote: body.Remote, Credential: body.Credential,
			Mirror: body.Mirror, IntervalSeconds: body.IntervalSeconds,
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		out := map[string]any{"repo": repoJSON(resp.GetRepo())}
		// A one-off import has no mirror, and the response says so with null
		// rather than an empty object that would read as "mirrored, from
		// nowhere".
		out["mirror"] = MirrorJSON(resp.GetMirror())
		WriteJSON(w, http.StatusCreated, out)
	}

	h["getMirror"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.GetMirror(r.Context(), &gitv1.GetMirrorRequest{Repo: chi.URLParam(r, "repo")})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, MirrorJSON(resp.GetMirror()))
	}

	h["setMirror"] = func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Remote          string `json:"remote"`
			Credential      string `json:"credential"`
			IntervalSeconds int32  `json:"interval_seconds"`
		}
		if err := decode(r, &body); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := c.SetMirror(r.Context(), &gitv1.SetMirrorRequest{
			Repo: chi.URLParam(r, "repo"), Remote: body.Remote,
			Credential: body.Credential, IntervalSeconds: body.IntervalSeconds,
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, MirrorJSON(resp.GetMirror()))
	}

	h["deleteMirror"] = func(w http.ResponseWriter, r *http.Request) {
		if _, err := c.DeleteMirror(r.Context(), &gitv1.DeleteMirrorRequest{
			Repo: chi.URLParam(r, "repo"),
		}); err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	}
}

// MirrorJSON renders one mirror. has_credential is the only thing said about the
// credential: whether one is set. The value itself is write-only, and a field
// carrying it here would leak it to every reader of the screen.
func MirrorJSON(m *gitv1.Mirror) map[string]any {
	if m == nil {
		return nil
	}
	return map[string]any{
		"repo_id":          m.GetRepoId(),
		"remote":           m.GetRemote(),
		"interval_seconds": m.GetIntervalSeconds(),
		"last_synced_at":   m.GetLastSyncedAt(),
		"last_error":       m.GetLastError(),
		"has_credential":   m.GetHasCredential(),
	}
}
