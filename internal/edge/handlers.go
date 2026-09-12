package edge

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
)

// Handlers builds the operation handlers for the wired service clients. The
// map is keyed by the operationId from the route table, so a route and its
// handler cannot be wired to different names.
func Handlers(cfg Config) map[string]http.HandlerFunc {
	h := map[string]http.HandlerFunc{}
	if cfg.Identity != nil {
		addIdentityHandlers(h, cfg.Identity)
	}
	if cfg.Git != nil {
		addGitHandlers(h, cfg.Git)
		addWorkHandlers(h, cfg.Git, cfg.Work, cfg.Reviews)
		addCIHandlers(h, cfg.Git, cfg.CI)
		addAgentHandlers(h, cfg.Git, cfg.Agents, cfg.Identity)
		addPlatformHandlers(h, cfg.Git, cfg.Graph, cfg.Gates)
		addRunToolHandlers(h, cfg.Git, cfg.Reviews, cfg.Agents)
		addWriteHandlers(h, cfg.Git, cfg.Work, cfg.CI)
	}
	h["healthz"] = func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
	return h
}

func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func addIdentityHandlers(h map[string]http.HandlerFunc, c identityv1.IdentityServiceClient) {
	h["register"] = func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Email, Username, Password string }
		if err := decode(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := c.Register(r.Context(), &identityv1.RegisterRequest{
			Email: req.Email, Username: req.Username, Password: req.Password,
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusCreated, map[string]string{"user_id": resp.GetUserId()})
	}

	h["login"] = func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username, Password string
			TOTPCode           string `json:"totp_code"`
		}
		if err := decode(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := c.Login(r.Context(), &identityv1.LoginRequest{
			Username: req.Username, Password: req.Password, TotpCode: req.TOTPCode,
		})
		if err != nil {
			// A TOTP challenge is not a failure the client should treat as bad
			// credentials, so the flag is carried through with the 401.
			WriteJSON(w, StatusFromGRPC(err), map[string]any{
				"error":         err.Error(),
				"requires_totp": true,
			})
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: "nf_session", Value: resp.GetSessionToken(),
			Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		WriteJSON(w, http.StatusOK, map[string]any{
			"session_token": resp.GetSessionToken(),
			"user_id":       resp.GetUserId(),
			"requires_totp": resp.GetRequiresTotp(),
		})
	}

	h["logout"] = func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "nf_session", Value: "", Path: "/", MaxAge: -1})
		WriteJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
	}

	h["createOrg"] = func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Name string }
		if err := decode(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := c.CreateOrg(r.Context(), &identityv1.CreateOrgRequest{Name: req.Name})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusCreated, map[string]string{
			"id": resp.GetOrg().GetId(), "name": resp.GetOrg().GetName(),
		})
	}

	h["getCurrentUser"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.GetCurrentUser(r.Context(), &identityv1.GetCurrentUserRequest{})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		u := resp.GetUser()
		WriteJSON(w, http.StatusOK, map[string]any{
			"id": u.GetId(), "email": u.GetEmail(),
			"username": u.GetUsername(), "totp_enabled": u.GetTotpEnabled(),
		})
	}

	h["listOrgs"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.ListOrgs(r.Context(), &identityv1.ListOrgsRequest{})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetOrgs()))
		for _, o := range resp.GetOrgs() {
			out = append(out, map[string]any{"id": o.GetId(), "name": o.GetName()})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"orgs": out})
	}

	h["getOrg"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.GetOrg(r.Context(), &identityv1.GetOrgRequest{Org: chi.URLParam(r, "org")})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"id": resp.GetOrg().GetId(), "name": resp.GetOrg().GetName(),
		})
	}

	h["listOrgMembers"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.ListOrgMembers(r.Context(), &identityv1.ListOrgMembersRequest{Org: chi.URLParam(r, "org")})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetMembers()))
		for _, m := range resp.GetMembers() {
			out = append(out, map[string]any{
				"user_id": m.GetUserId(), "username": m.GetUsername(), "role": m.GetRole(),
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"members": out})
	}

	h["addSSHKey"] = func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Title, Key string }
		if err := decode(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := c.AddSSHKey(r.Context(), &identityv1.AddSSHKeyRequest{Title: req.Title, Key: req.Key})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusCreated, map[string]any{
			"id": resp.GetKey().GetId(), "title": resp.GetKey().GetTitle(),
			"fingerprint": resp.GetKey().GetFingerprint(),
		})
	}

	h["listSSHKeys"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.ListSSHKeys(r.Context(), &identityv1.ListSSHKeysRequest{})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetKeys()))
		for _, k := range resp.GetKeys() {
			out = append(out, map[string]any{
				"id": k.GetId(), "title": k.GetTitle(), "fingerprint": k.GetFingerprint(),
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"keys": out})
	}

	h["deleteSSHKey"] = func(w http.ResponseWriter, r *http.Request) {
		if _, err := c.DeleteSSHKey(r.Context(), &identityv1.DeleteSSHKeyRequest{Id: chi.URLParam(r, "id")}); err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	}

	h["createToken"] = func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name       string   `json:"name"`
			Scopes     []string `json:"scopes"`
			TTLSeconds int64    `json:"ttl_seconds"`
		}
		if err := decode(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := c.CreateToken(r.Context(), &identityv1.CreateTokenRequest{
			Name: req.Name, Scopes: req.Scopes, TtlSeconds: req.TTLSeconds,
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		// The plaintext is returned exactly once, here: only its hash is stored.
		WriteJSON(w, http.StatusCreated, map[string]any{
			"id": resp.GetToken().GetId(), "name": resp.GetToken().GetName(),
			"token": resp.GetPlaintext(),
		})
	}

	h["listTokens"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.ListTokens(r.Context(), &identityv1.ListTokensRequest{})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetTokens()))
		for _, t := range resp.GetTokens() {
			out = append(out, map[string]any{
				"id": t.GetId(), "name": t.GetName(),
				"scopes": t.GetScopes(), "expires_at": t.GetExpiresAt(),
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"tokens": out})
	}

	h["deleteToken"] = func(w http.ResponseWriter, r *http.Request) {
		if _, err := c.DeleteToken(r.Context(), &identityv1.DeleteTokenRequest{Id: chi.URLParam(r, "id")}); err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
	}

	h["setup2FA"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.Setup2FA(r.Context(), &identityv1.Setup2FARequest{})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"secret": resp.GetSecret(), "uri": resp.GetUri()})
	}

	h["verify2FA"] = func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code string `json:"code"`
		}
		if err := decode(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := c.Verify2FA(r.Context(), &identityv1.Verify2FARequest{Code: req.Code})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"enabled": resp.GetEnabled()})
	}

	h["addOrgMember"] = func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID string `json:"user_id"`
			Role   string `json:"role"`
		}
		if err := decode(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		_, err := c.AddOrgMember(r.Context(), &identityv1.AddOrgMemberRequest{
			OrgId: chi.URLParam(r, "org"), UserId: req.UserID, Role: req.Role,
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusCreated, map[string]string{"status": "added"})
	}
}

func addGitHandlers(h map[string]http.HandlerFunc, c gitv1.GitServiceClient) {
	h["createRepo"] = func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Name string }
		if err := decode(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := c.CreateRepo(r.Context(), &gitv1.CreateRepoRequest{Name: req.Name})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusCreated, repoJSON(resp.GetRepo()))
	}

	h["listRepos"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.ListRepos(r.Context(), &gitv1.ListReposRequest{})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetRepos()))
		for _, rp := range resp.GetRepos() {
			out = append(out, repoJSON(rp))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"repos": out})
	}

	h["getRepo"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.GetRepo(r.Context(), &gitv1.GetRepoRequest{Name: chi.URLParam(r, "repo")})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, repoJSON(resp.GetRepo()))
	}

	h["deleteRepo"] = func(w http.ResponseWriter, r *http.Request) {
		if _, err := c.DeleteRepo(r.Context(), &gitv1.DeleteRepoRequest{Name: chi.URLParam(r, "repo")}); err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	}

	h["listBranches"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.ListBranches(r.Context(), &gitv1.ListBranchesRequest{Repo: chi.URLParam(r, "repo")})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"refs": refsJSON(resp.GetRefs())})
	}

	h["listTags"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.ListTags(r.Context(), &gitv1.ListTagsRequest{Repo: chi.URLParam(r, "repo")})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"refs": refsJSON(resp.GetRefs())})
	}

	h["listCommits"] = func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		resp, err := c.ListCommits(r.Context(), &gitv1.ListCommitsRequest{
			Repo: chi.URLParam(r, "repo"), Ref: refParam(r), Limit: int32(limit),
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetCommits()))
		for _, cm := range resp.GetCommits() {
			out = append(out, map[string]any{
				"sha": cm.GetSha(), "message": cm.GetMessage(),
				"author_name": cm.GetAuthorName(), "author_email": cm.GetAuthorEmail(),
				"at": cm.GetAt(),
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"commits": out})
	}

	h["getTree"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.GetTree(r.Context(), &gitv1.GetTreeRequest{
			Repo: chi.URLParam(r, "repo"), Ref: refParam(r), Path: chi.URLParam(r, "*"),
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		out := make([]map[string]any, 0, len(resp.GetEntries()))
		for _, e := range resp.GetEntries() {
			out = append(out, map[string]any{
				"mode": e.GetMode(), "kind": e.GetKind(), "sha": e.GetSha(),
				"name": e.GetName(), "size": e.GetSize(),
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"entries": out})
	}

	h["getBlob"] = func(w http.ResponseWriter, r *http.Request) {
		resp, err := c.GetBlob(r.Context(), &gitv1.GetBlobRequest{
			Repo: chi.URLParam(r, "repo"), Ref: refParam(r), Path: chi.URLParam(r, "*"),
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp.GetContent())
	}

	h["getDiff"] = func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		resp, err := c.GetDiff(r.Context(), &gitv1.GetDiffRequest{
			Repo: chi.URLParam(r, "repo"), From: q.Get("from"), To: q.Get("to"),
		})
		if err != nil {
			WriteError(w, StatusFromGRPC(err), err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"unified": resp.GetUnified()})
	}
}

func repoJSON(r *gitv1.Repo) map[string]any {
	return map[string]any{
		"id": r.GetId(), "org_id": r.GetOrgId(),
		"name": r.GetName(), "default_branch": r.GetDefaultBranch(),
	}
}

func refsJSON(refs []*gitv1.Ref) []map[string]any {
	out := make([]map[string]any, 0, len(refs))
	for _, rf := range refs {
		out = append(out, map[string]any{"name": rf.GetName(), "sha": rf.GetSha(), "kind": rf.GetKind()})
	}
	return out
}

// refParam reads the {ref} path segment and undoes its percent-encoding.
//
// A ref is one path segment in these routes, but a git ref routinely contains
// slashes — every branch this platform's own agents write to is
// "agents/<work item>/work". A client has to encode those slashes to keep the
// ref in one segment, and chi hands the segment back still encoded, so
// without this the repository browser cannot open any branch an agent wrote.
func refParam(r *http.Request) string {
	raw := chi.URLParam(r, "ref")
	if decoded, err := url.PathUnescape(raw); err == nil {
		return decoded
	}
	return raw
}
