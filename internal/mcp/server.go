package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Caller is the authenticated subject behind an MCP request. Every tool is
// answered within this caller's organization.
type Caller struct {
	UserID string
	OrgID  string

	// orgRef is the organization as the token named it, and credential the
	// credential it carried. They stay unexported so only this package's
	// backend can present them onward, and only for the request that
	// authenticated them.
	orgRef     string
	credential string
}

// Backend is the platform surface the MCP tools expose. It is an interface so
// the protocol layer is testable without standing up every service, and so the
// server holds no direct dependency on any single one.
type Backend interface {
	Authenticate(ctx context.Context, token string) (Caller, error)
	GetWorkItem(ctx context.Context, c Caller, org, repo, key string) (string, error)
	SearchRepository(ctx context.Context, c Caller, org, repo, query string) (string, error)
	GetSymbol(ctx context.Context, c Caller, org, repo, name string) (string, error)
	CreateBranch(ctx context.Context, c Caller, org, repo, branch, from string) (string, error)
	GetReview(ctx context.Context, c Caller, org, repo string, number int) (string, error)
	RunCI(ctx context.Context, c Caller, org, repo, ref string) (string, error)
	GetGateStatus(ctx context.Context, c Caller, org, repo string, number int) (string, error)
}

// Server speaks the current MCP specification revision over stdio and
// Streamable HTTP. The deprecated HTTP+SSE transport is not implemented.
type Server struct {
	backend Backend

	mu      sync.RWMutex
	token   string
	origins []string
}

// NewServer returns a server backed by b.
func NewServer(b Backend) *Server { return &Server{backend: b} }

// SetToken sets the credential used for stdio sessions, where there is no
// per-request header to carry it.
func (s *Server) SetToken(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = t
}

// SetAllowedOrigins names the browser origins that may reach the Streamable
// HTTP transport. A request carrying any other Origin is refused.
func (s *Server) SetAllowedOrigins(origins []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.origins = append([]string(nil), origins...)
}

func (s *Server) currentToken() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token
}

// ServeStdio reads newline-delimited JSON-RPC from in and writes responses to
// out until in is exhausted or ctx is cancelled.
func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			if err := enc.Encode(newError(nil, CodeParseError, err.Error())); err != nil {
				return err
			}
			continue
		}
		resp := s.dispatch(ctx, req, s.currentToken())
		if resp == nil {
			continue // notification
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

// ServeHTTP implements the Streamable HTTP transport: one JSON-RPC message per
// POST, answered inline.
//
// The transport rules of the 2025-06-18 revision are enforced here rather than
// assumed: a browser Origin not explicitly allowed is refused, because a page
// reaching this port through DNS rebinding would otherwise drive the platform;
// an MCP-Protocol-Version header naming another revision is a 400, since this
// server speaks exactly one; and a call refused for want of a credential is an
// HTTP 401 with a Bearer challenge, which is what an HTTP client acts on.
// A GET opens no server-initiated stream here, so it is a 405, as the
// transport permits.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(origin) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "only POST is supported by the Streamable HTTP transport", http.StatusMethodNotAllowed)
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && v != ProtocolVersion {
		writeJSON(w, http.StatusBadRequest, newError(nil, CodeInvalidRequest,
			fmt.Sprintf("unsupported MCP-Protocol-Version %q; this server speaks %s only", v, ProtocolVersion)))
		return
	}
	defer r.Body.Close()

	var req Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMessageBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, newError(nil, CodeParseError, err.Error()))
		return
	}
	token := s.currentToken()
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		token = strings.TrimPrefix(h, "Bearer ")
	}
	resp := s.dispatch(r.Context(), req, token)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if resp.Error != nil && resp.Error.Code == CodeUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="novaforge", error="invalid_token"`)
		writeJSON(w, http.StatusUnauthorized, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// maxMessageBytes bounds one JSON-RPC message, matching the stdio reader.
const maxMessageBytes = 8 << 20

func (s *Server) originAllowed(origin string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, o := range s.origins {
		if o == origin {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) dispatch(ctx context.Context, req Request, token string) *Response {
	if req.ID == nil && strings.HasPrefix(req.Method, "notifications/") {
		return nil
	}
	switch req.Method {
	case "initialize":
		return newResponse(req.ID, map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "novaforge", "version": "0.1.0"},
		})
	case "ping":
		return newResponse(req.ID, map[string]any{})
	case "tools/list":
		return newResponse(req.ID, map[string]any{"tools": toolDefs()})
	case "tools/call":
		return s.callTool(ctx, req, token)
	default:
		return newError(req.ID, CodeMethodNotFound, fmt.Sprintf("unknown method %q", req.Method))
	}
}

func (s *Server) callTool(ctx context.Context, req Request, token string) *Response {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return newError(req.ID, CodeInvalidParams, err.Error())
	}

	caller, err := s.backend.Authenticate(ctx, token)
	if err != nil {
		return newError(req.ID, CodeUnauthorized, "unauthorized: "+err.Error())
	}

	str := func(k string) string {
		v, _ := params.Arguments[k].(string)
		return v
	}
	num := func(k string) int {
		switch v := params.Arguments[k].(type) {
		case float64:
			return int(v)
		case int:
			return v
		}
		return 0
	}

	var out string
	switch params.Name {
	case "novaforge.get_work_item":
		out, err = s.backend.GetWorkItem(ctx, caller, str("org"), str("repo"), str("key"))
	case "novaforge.search_repository":
		out, err = s.backend.SearchRepository(ctx, caller, str("org"), str("repo"), str("query"))
	case "novaforge.get_symbol":
		out, err = s.backend.GetSymbol(ctx, caller, str("org"), str("repo"), str("name"))
	case "novaforge.create_branch":
		out, err = s.backend.CreateBranch(ctx, caller, str("org"), str("repo"), str("branch"), str("from"))
	case "novaforge.get_review":
		out, err = s.backend.GetReview(ctx, caller, str("org"), str("repo"), num("number"))
	case "novaforge.run_ci":
		out, err = s.backend.RunCI(ctx, caller, str("org"), str("repo"), str("ref"))
	case "novaforge.get_gate_status":
		out, err = s.backend.GetGateStatus(ctx, caller, str("org"), str("repo"), num("number"))
	default:
		return newError(req.ID, CodeMethodNotFound, fmt.Sprintf("unknown tool %q", params.Name))
	}
	if err != nil {
		// A tool failure is reported as a tool result, not a protocol error, so
		// the calling agent can see it and react rather than losing the session.
		return newResponse(req.ID, CallResult{
			Content: []Content{{Type: "text", Text: err.Error()}},
			IsError: true,
		})
	}
	return newResponse(req.ID, CallResult{Content: []Content{{Type: "text", Text: out}}})
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func toolDefs() []ToolDef {
	defs := []ToolDef{
		{"novaforge.get_work_item", "Fetch a Work Item by key", schema(
			map[string]any{"org": strProp("organization"), "repo": strProp("repository"), "key": strProp("Work Item key, e.g. NF-182")},
			"org", "repo", "key")},
		{"novaforge.search_repository", "Search a repository's code", schema(
			map[string]any{"org": strProp("organization"), "repo": strProp("repository"), "query": strProp("search query")},
			"org", "repo", "query")},
		{"novaforge.get_symbol", "Resolve a symbol and its relationships", schema(
			map[string]any{"org": strProp("organization"), "repo": strProp("repository"), "name": strProp("symbol name")},
			"org", "repo", "name")},
		{"novaforge.create_branch", "Create a branch", schema(
			map[string]any{"org": strProp("organization"), "repo": strProp("repository"), "branch": strProp("new branch"), "from": strProp("base ref")},
			"org", "repo", "branch")},
		{"novaforge.get_review", "Read an Engineering Run", schema(
			map[string]any{"org": strProp("organization"), "repo": strProp("repository"), "number": intProp("run number")},
			"org", "repo", "number")},
		{"novaforge.run_ci", "Trigger CI for a ref", schema(
			map[string]any{"org": strProp("organization"), "repo": strProp("repository"), "ref": strProp("git ref")},
			"org", "repo", "ref")},
		{"novaforge.get_gate_status", "Read gate status for a run", schema(
			map[string]any{"org": strProp("organization"), "repo": strProp("repository"), "number": intProp("run number")},
			"org", "repo", "number")},
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

func schema(props map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

// ToolDefs is the tool surface this server advertises. It is exported so the
// REST edge can show a person exactly what an external agent will discover —
// one list, so the two cannot disagree.
func ToolDefs() []ToolDef { return toolDefs() }
