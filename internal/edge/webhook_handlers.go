package edge

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

// addWebhookHandlers mounts the webhook surface. It lives with the repository
// routes because a hook belongs to a repository, and it goes through git-platform
// for the same reason.
//
// No response here carries a secret, and no handler logs a request body: the
// secret arrives in one, and a log line is a read like any other.
func addWebhookHandlers(h map[string]http.HandlerFunc, c gitv1.GitServiceClient) {
	h["listHooks"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.ListHooks(r.Context(), &gitv1.ListHooksRequest{Repo: chi.URLParam(r, "repo")})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetHooks()))
		for _, hook := range resp.GetHooks() {
			out = append(out, HookJSON(hook))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"hooks": out})
	}

	h["createHook"] = func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL    string   `json:"url"`
			Events []string `json:"events"`
			Secret string   `json:"secret"`
		}
		if err := decode(r, &body); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := c.CreateHook(r.Context(), &gitv1.CreateHookRequest{
			Repo: chi.URLParam(r, "repo"), Url: body.URL, Events: body.Events, Secret: body.Secret,
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusCreated, HookJSON(resp.GetHook()))
	}

	h["updateHook"] = func(w http.ResponseWriter, r *http.Request) {
		// Both fields are optional and applied only when present, for the reason
		// the repository PATCH carries its own flags: rotating a secret must not
		// also switch the hook off, and switching it off must not strip the
		// secret and turn every later delivery into an unsigned one.
		var body struct {
			Secret *string `json:"secret"`
			Active *bool   `json:"active"`
		}
		if err := decode(r, &body); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		req := &gitv1.SetHookSecretRequest{Repo: chi.URLParam(r, "repo"), Id: chi.URLParam(r, "id")}
		if body.Secret != nil {
			req.Secret, req.SetSecret = *body.Secret, true
		}
		if body.Active != nil {
			req.Active, req.SetActive = *body.Active, true
		}
		resp, err := c.SetHookSecret(r.Context(), req)
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, HookJSON(resp.GetHook()))
	}

	h["deleteHook"] = func(w http.ResponseWriter, r *http.Request) {
		if _, err := c.DeleteHook(r.Context(), &gitv1.DeleteHookRequest{
			Repo: chi.URLParam(r, "repo"), Id: chi.URLParam(r, "id"),
		}); err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	}

	h["listHookDeliveries"] = func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		resp, err := c.ListHookDeliveries(r.Context(), &gitv1.ListHookDeliveriesRequest{
			Repo: chi.URLParam(r, "repo"), HookId: chi.URLParam(r, "id"), Limit: int32(limit),
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetDeliveries()))
		for _, d := range resp.GetDeliveries() {
			out = append(out, map[string]any{
				"id":          d.GetId(),
				"hook_id":     d.GetHookId(),
				"event":       d.GetEvent(),
				"status_code": d.GetStatusCode(),
				"error":       d.GetError(),
				"attempt":     d.GetAttempt(),
				"at":          d.GetAt(),
				// Whether an attempt counts as delivered is decided in one place
				// — internal/webhooks decides it the same way when it chooses to
				// retry — so the GUI cannot disagree with the retry logic about
				// what succeeded.
				"delivered": d.GetStatusCode() >= 200 && d.GetStatusCode() < 300,
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"deliveries": out})
	}
}

// HookJSON renders one webhook. has_secret is the only thing said about the
// secret: whoever set it has it, and nothing on the platform can show it again.
func HookJSON(h *gitv1.Hook) map[string]any {
	events := h.GetEvents()
	if events == nil {
		// An absent list and an empty one mean the same thing here — every event —
		// but a JSON null would make a client check for it before iterating.
		events = []string{}
	}
	return map[string]any{
		"id":         h.GetId(),
		"repo_id":    h.GetRepoId(),
		"url":        h.GetUrl(),
		"events":     events,
		"active":     h.GetActive(),
		"has_secret": h.GetHasSecret(),
		"created_at": h.GetCreatedAt(),
	}
}
