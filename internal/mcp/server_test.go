package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/novaforge/novaforge/internal/mcp"
)

// backend is a deterministic in-process implementation of the platform calls
// the MCP tools make. The services themselves are tested separately; this
// exercises the MCP protocol surface.
type backend struct {
	authErr error
	calls   []string
}

func (b *backend) Authenticate(_ context.Context, token string) (mcp.Caller, error) {
	if b.authErr != nil {
		return mcp.Caller{}, b.authErr
	}
	if token == "" {
		return mcp.Caller{}, errors.New("unauthorized: no token")
	}
	if token == "other-org" {
		return mcp.Caller{UserID: "u2", OrgID: "org-b"}, nil
	}
	return mcp.Caller{UserID: "u1", OrgID: "org-a"}, nil
}

func (b *backend) GetWorkItem(_ context.Context, c mcp.Caller, org, repo, key string) (string, error) {
	b.calls = append(b.calls, "GetWorkItem")
	if org != c.OrgID {
		return "", errors.New("denied: cross-org access")
	}
	return `{"key":"` + key + `","goal":"add passkeys"}`, nil
}
func (b *backend) SearchRepository(_ context.Context, c mcp.Caller, org, repo, q string) (string, error) {
	b.calls = append(b.calls, "SearchRepository")
	return `{"hits":[]}`, nil
}
func (b *backend) GetSymbol(_ context.Context, c mcp.Caller, org, repo, name string) (string, error) {
	b.calls = append(b.calls, "GetSymbol")
	return `{"symbol":"` + name + `"}`, nil
}
func (b *backend) CreateBranch(_ context.Context, c mcp.Caller, org, repo, branch, from string) (string, error) {
	b.calls = append(b.calls, "CreateBranch")
	return `{"branch":"` + branch + `"}`, nil
}
func (b *backend) GetReview(_ context.Context, c mcp.Caller, org, repo string, number int) (string, error) {
	b.calls = append(b.calls, "GetReview")
	return `{"number":1}`, nil
}
func (b *backend) RunCI(_ context.Context, c mcp.Caller, org, repo, ref string) (string, error) {
	b.calls = append(b.calls, "RunCI")
	return `{"run":"queued"}`, nil
}
func (b *backend) GetGateStatus(_ context.Context, c mcp.Caller, org, repo string, number int) (string, error) {
	b.calls = append(b.calls, "GetGateStatus")
	return `{"allowed":false,"reasons":["tests not evaluated"]}`, nil
}

func roundTripStdio(t *testing.T, s *mcp.Server, reqs ...any) []mcp.Response {
	t.Helper()
	var in bytes.Buffer
	for _, r := range reqs {
		b, _ := json.Marshal(r)
		in.Write(b)
		in.WriteByte('\n')
	}
	var out bytes.Buffer
	if err := s.ServeStdio(context.Background(), &in, &out); err != nil {
		t.Fatalf("ServeStdio: %v", err)
	}
	var resps []mcp.Response
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var r mcp.Response
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		resps = append(resps, r)
	}
	return resps
}

func TestInitializeAdvertisesCurrentProtocolVersion(t *testing.T) {
	s := mcp.NewServer(&backend{})
	resps := roundTripStdio(t, s, mcp.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize",
	})
	if len(resps) != 1 || resps[0].Error != nil {
		t.Fatalf("initialize failed: %+v", resps)
	}
	raw, _ := json.Marshal(resps[0].Result)
	if !strings.Contains(string(raw), mcp.ProtocolVersion) {
		t.Fatalf("want protocol version %s advertised, got %s", mcp.ProtocolVersion, raw)
	}
	// The deprecated transport must not be offered anywhere.
	if strings.Contains(strings.ToLower(string(raw)), "sse") {
		t.Fatalf("deprecated HTTP+SSE transport must not be advertised: %s", raw)
	}
}

func TestToolsListReturnsExactlySeven(t *testing.T) {
	s := mcp.NewServer(&backend{})
	resps := roundTripStdio(t, s, mcp.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list",
	})
	raw, _ := json.Marshal(resps[0].Result)
	var res struct {
		Tools []mcp.ToolDef `json:"tools"`
	}
	json.Unmarshal(raw, &res)
	got := make([]string, 0, len(res.Tools))
	for _, td := range res.Tools {
		got = append(got, td.Name)
	}
	sort.Strings(got)
	want := []string{
		"novaforge.create_branch", "novaforge.get_gate_status", "novaforge.get_review",
		"novaforge.get_symbol", "novaforge.get_work_item", "novaforge.run_ci",
		"novaforge.search_repository",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tool set drifted:\n got %v\nwant %v", got, want)
	}
}

func TestCallToolRequiresAuth(t *testing.T) {
	s := mcp.NewServer(&backend{})
	params, _ := json.Marshal(map[string]any{
		"name":      "novaforge.get_work_item",
		"arguments": map[string]any{"org": "org-a", "repo": "r", "key": "NF-1"},
	})
	resps := roundTripStdio(t, s, mcp.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: params,
	})
	if resps[0].Error == nil {
		t.Fatal("want an error without a token")
	}
	if !strings.Contains(strings.ToLower(resps[0].Error.Message), "unauthorized") {
		t.Fatalf("want 'unauthorized' in the message, got %q", resps[0].Error.Message)
	}
}

func TestCallToolIsOrgScoped(t *testing.T) {
	b := &backend{}
	s := mcp.NewServer(b)
	s.SetToken("other-org") // resolves to org-b
	params, _ := json.Marshal(map[string]any{
		"name":      "novaforge.get_work_item",
		"arguments": map[string]any{"org": "org-a", "repo": "r", "key": "NF-1"},
	})
	resps := roundTripStdio(t, s, mcp.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: params,
	})
	raw, _ := json.Marshal(resps[0].Result)
	if resps[0].Error == nil && !strings.Contains(string(raw), "denied") {
		t.Fatalf("a caller in org-b must not read org-a: %v %s", resps[0].Error, raw)
	}
}

func TestStreamableHTTPRoundTrip(t *testing.T) {
	s := mcp.NewServer(&backend{})
	srv := httptest.NewServer(s)
	defer srv.Close()

	body, _ := json.Marshal(mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list"})
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var r mcp.Response
	json.NewDecoder(resp.Body).Decode(&r)
	raw, _ := json.Marshal(r.Result)
	if !strings.Contains(string(raw), "novaforge.get_work_item") {
		t.Fatalf("Streamable HTTP returned a different tool set: %s", raw)
	}
}

// postMCP sends one JSON-RPC message the way a Streamable HTTP client does.
func postMCP(t *testing.T, url string, msg any, headers map[string]string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(msg)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// TestStreamableHTTPTransportRules pins what the 2025-06-18 Streamable HTTP
// transport requires of a server, beyond answering a POST: a browser origin
// is refused (DNS rebinding), a protocol version header naming a revision
// this server does not speak is a 400, a notification is a bodiless 202, a
// GET that opens no stream is a 405, and a call that needs a credential and
// has none is an HTTP 401 rather than a 200 carrying an error.
func TestStreamableHTTPTransportRules(t *testing.T) {
	s := mcp.NewServer(&backend{})
	s.SetAllowedOrigins([]string{"https://agents.example.com"})
	srv := httptest.NewServer(s)
	defer srv.Close()
	list := mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list"}

	if r := postMCP(t, srv.URL, list, map[string]string{"Origin": "https://evil.example.net"}); r.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Origin: status %d, want 403", r.StatusCode)
	}
	if r := postMCP(t, srv.URL, list, map[string]string{"Origin": "https://agents.example.com"}); r.StatusCode != http.StatusOK {
		t.Errorf("allowed Origin: status %d, want 200", r.StatusCode)
	}
	if r := postMCP(t, srv.URL, list, map[string]string{"MCP-Protocol-Version": "2024-11-05"}); r.StatusCode != http.StatusBadRequest {
		t.Errorf("unsupported MCP-Protocol-Version: status %d, want 400", r.StatusCode)
	}
	if r := postMCP(t, srv.URL, list, map[string]string{"MCP-Protocol-Version": mcp.ProtocolVersion}); r.StatusCode != http.StatusOK {
		t.Errorf("current MCP-Protocol-Version: status %d, want 200", r.StatusCode)
	}
	note := mcp.Request{JSONRPC: "2.0", Method: "notifications/initialized"}
	if r := postMCP(t, srv.URL, note, nil); r.StatusCode != http.StatusAccepted {
		t.Errorf("notification: status %d, want 202", r.StatusCode)
	}
	get, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	get.Body.Close()
	if get.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d, want 405", get.StatusCode)
	}
	params, _ := json.Marshal(map[string]any{"name": "novaforge.get_work_item", "arguments": map[string]any{"org": "org-a", "repo": "r", "key": "NF-1"}})
	call := mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/call", Params: params}
	r := postMCP(t, srv.URL, call, nil)
	if r.StatusCode != http.StatusUnauthorized {
		t.Errorf("call with no credential: status %d, want 401", r.StatusCode)
	}
	if !strings.HasPrefix(r.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("401 without a Bearer challenge: %q", r.Header.Get("WWW-Authenticate"))
	}
}

// TestInitializeNegotiatesOnlyTheCurrentRevision: a client asking for an older
// revision is answered with the one revision this server speaks, which the
// client then accepts or disconnects — there is no legacy mode to fall into.
func TestInitializeNegotiatesOnlyTheCurrentRevision(t *testing.T) {
	s := mcp.NewServer(&backend{})
	params, _ := json.Marshal(map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "old", "version": "1"}})
	resps := roundTripStdio(t, s, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize", Params: params})
	raw, _ := json.Marshal(resps[0].Result)
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(raw, &res)
	if res.ProtocolVersion != mcp.ProtocolVersion {
		t.Fatalf("negotiated %q, want %q", res.ProtocolVersion, mcp.ProtocolVersion)
	}
}

func TestUnknownMethodIsMethodNotFound(t *testing.T) {
	s := mcp.NewServer(&backend{})
	resps := roundTripStdio(t, s, mcp.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "nonsense/method",
	})
	if resps[0].Error == nil || resps[0].Error.Code != mcp.CodeMethodNotFound {
		t.Fatalf("want CodeMethodNotFound, got %+v", resps[0].Error)
	}
}
