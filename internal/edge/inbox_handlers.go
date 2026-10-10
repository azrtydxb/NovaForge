package edge

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
)

// repoNames maps the work schema's repository ids onto names for a page of
// notifications, the same join the approval handlers make: a URL of uuids is
// not a URL a person reads.
func repoNames(ctx context.Context, g gitv1.GitServiceClient) map[string]string {
	names := map[string]string{}
	if g == nil {
		return names
	}
	if resp, err := g.ListRepos(ctx, &gitv1.ListReposRequest{}); err == nil {
		for _, repo := range resp.GetRepos() {
			names[repo.GetId()] = repo.GetName()
		}
	}
	return names
}

// inboxLink renders the deep link a notification opens. The screens route by
// repository name plus run number or Work Item key — which is exactly what
// ref and repo carry, so the link is derived, never stored and left stale.
func inboxLink(reason, repo, ref string) string {
	if repo == "" {
		return ""
	}
	n := refNumber(ref)
	switch {
	case n > 0:
		return "/runs/" + repo + "/" + strconv.Itoa(n)
	case reason == "maintenance":
		return "/maintenance"
	}
	return ""
}

// refNumber extracts the number from a "run #212" ref; 0 when the ref names
// something else (a Work Item key, a maintenance id).
func refNumber(ref string) int {
	i := strings.Index(ref, "#")
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(ref[i+1:]))
	if err != nil {
		return 0
	}
	return n
}

// InboxJSON renders one notification for the GUI. names maps repository ids
// onto names; a repository the caller cannot see (deleted, or renamed in
// another org's scope) is an empty name, not a guessed one.
func InboxJSON(n *workv1.InboxNotification, names map[string]string) map[string]any {
	repo := names[n.GetRepoId()]
	return map[string]any{
		"id":         n.GetId(),
		"repo_id":    n.GetRepoId(),
		"repo":       repo,
		"reason":     n.GetReason(),
		"ref":        n.GetRef(),
		"title":      n.GetTitle(),
		"body":       n.GetBody(),
		"actor_id":   n.GetActorId(),
		"actor_kind": n.GetActorKind(),
		"actor_name": n.GetActorName(),
		"state":      n.GetState(),
		"snoozed":    n.GetSnoozedUntil() != "",
		"created_at": n.GetCreatedAt(),
		"link":       inboxLink(n.GetReason(), repo, n.GetRef()),
	}
}

// addInboxHandlers mounts the unified Inbox. Every route that touches a row
// is org-scoped like the rest of the API; /api/v1/inbox/unread alone is
// answered from the caller's own recipient id and spans their organizations,
// because the header badge is one number that cannot know which org it came
// from (see work.Store.InboxUnread).
func addInboxHandlers(h map[string]http.HandlerFunc, w workv1.WorkServiceClient, g gitv1.GitServiceClient) {
	if w == nil {
		return
	}
	h["inboxUnread"] = func(wr http.ResponseWriter, r *http.Request) {
		resp, err := w.InboxUnread(r.Context(), &workv1.InboxUnreadRequest{})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusOK, map[string]int{"unread": int(resp.GetUnread())})
	}

	h["listInbox"] = func(wr http.ResponseWriter, r *http.Request) {
		var repoArg string
		if name := r.URL.Query().Get("repo"); name != "" {
			repoResp, err := g.GetRepo(r.Context(), &gitv1.GetRepoRequest{Name: name})
			if err != nil {
				WriteError(wr, StatusFromGRPC(err), err)
				return
			}
			repoArg = repoResp.GetRepo().GetId()
		}
		state := r.URL.Query().Get("state")
		if state == "" {
			state = "inbox"
		}
		limit := 0
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil {
				limit = n
			}
		}
		resp, err := w.ListInbox(r.Context(), &workv1.ListInboxRequest{
			State: state, Reason: r.URL.Query().Get("reason"),
			RepoId: repoArg, Limit: int32(limit),
		})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		names := repoNames(r.Context(), g)
		out := make([]map[string]any, 0, len(resp.GetNotifications()))
		for _, n := range resp.GetNotifications() {
			out = append(out, InboxJSON(n, names))
		}
		WriteJSON(wr, http.StatusOK, map[string]any{"notifications": out})
	}

	// done, snooze and save each address one of the caller's own rows; the
	// service refuses anything else as not found, so a guessed id in another
	// organization answers the same as a wrong one.
	h["inboxDone"] = func(wr http.ResponseWriter, r *http.Request) {
		_, err := w.InboxDone(r.Context(), &workv1.InboxDoneRequest{Id: chi.URLParam(r, "id")})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusOK, map[string]bool{"ok": true})
	}

	h["inboxSave"] = func(wr http.ResponseWriter, r *http.Request) {
		var body struct {
			Saved bool `json:"saved"`
		}
		if err := decode(r, &body); err != nil {
			WriteError(wr, http.StatusBadRequest, err)
			return
		}
		_, err := w.InboxSave(r.Context(), &workv1.InboxSaveRequest{Id: chi.URLParam(r, "id"), Saved: body.Saved})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusOK, map[string]bool{"ok": true})
	}

	h["inboxSnooze"] = func(wr http.ResponseWriter, r *http.Request) {
		var body struct {
			Until string `json:"until"`
		}
		if err := decode(r, &body); err != nil {
			WriteError(wr, http.StatusBadRequest, err)
			return
		}
		until := time.Now().Add(24 * time.Hour)
		if body.Until != "" {
			t, perr := time.Parse(time.RFC3339, body.Until)
			if perr != nil {
				WriteError(wr, http.StatusBadRequest, perr)
				return
			}
			until = t
		}
		_, err := w.InboxSnooze(r.Context(), &workv1.InboxSnoozeRequest{
			Id: chi.URLParam(r, "id"), Until: until.Format(time.RFC3339),
		})
		if err != nil {
			WriteError(wr, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(wr, http.StatusOK, map[string]bool{"ok": true})
	}
}
