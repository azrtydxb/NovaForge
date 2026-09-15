package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/mcp"
	"github.com/novaforge/novaforge/internal/platformtest"
)

// operationsFixture is an organization with an indexed repository, a Work
// Item and an open Engineering Run: everything the four operations read.
type operationsFixture struct {
	p      *platformtest.Platform
	owner  platformtest.User
	org    platformtest.Org
	repo   platformtest.Repo
	key    string
	run    int32
	runID  string
	branch string
}

func newOperationsFixture(t *testing.T) operationsFixture {
	t.Helper()
	p := platformtest.Start(t)
	owner := p.NewUser(t, "mcpops")
	org := p.NewOrg(t, owner, "mcpopsorg")
	repo := p.NewRepo(t, owner, org, "ledger", map[string]string{
		"billing/vat.go": "package billing\n\n// VATTotal adds value-added tax to a net amount.\nfunc VATTotal(net float64) float64 { return net * 1.21 }\n",
		"README.md":      "# ledger\n",
	})
	p.Index(t, org, repo, repo.Head)
	item := p.NewWorkItem(t, owner, org, repo)

	ctx := p.AsUser(owner, org)
	if _, err := p.Git.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: repo.Name, Name: "feature/vat"}); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	run, err := p.Reviews.CreateRun(ctx, &reviewsv1.CreateRunRequest{
		RepoId: repo.ID, WorkItemId: item.GetId(), Title: "VAT", SourceRef: "feature/vat", TargetRef: "main",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return operationsFixture{p: p, owner: owner, org: org, repo: repo, key: item.GetKey(),
		run: run.GetRun().GetNumber(), runID: run.GetRun().GetId()}
}

// call is one tool invocation and what its successful text must contain.
type call struct {
	tool string
	args map[string]any
	want []string
}

func (f operationsFixture) calls(branch string) []call {
	base := func(extra map[string]any) map[string]any {
		m := map[string]any{"org": f.org.Name, "repo": f.repo.Name}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	return []call{
		{"novaforge.get_work_item", base(map[string]any{"key": f.key}), []string{f.key, "describe the repository"}},
		{"novaforge.search_repository", base(map[string]any{"query": "VATTotal"}), []string{"billing/vat.go"}},
		{"novaforge.create_branch", base(map[string]any{"branch": branch}), []string{branch, f.repo.Head}},
		{"novaforge.get_gate_status", base(map[string]any{"number": f.run}), []string{`"may_merge"`, `"evaluations"`}},
	}
}

// assertBranch checks create_branch really created the branch, through the
// git service, not only that the tool said so.
func (f operationsFixture) assertBranch(t *testing.T, branch string) {
	t.Helper()
	refs, err := f.p.Git.ListBranches(f.p.AsUser(f.owner, f.org), &gitv1.ListBranchesRequest{Repo: f.repo.Name})
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	for _, r := range refs.GetRefs() {
		if r.GetName() == branch && r.GetSha() == f.repo.Head {
			return
		}
	}
	t.Fatalf("branch %s does not exist at %s after create_branch", branch, f.repo.Head)
}

// TestMCPServerOperations: an external MCP client retrieves a Work Item,
// searches a repository, creates a branch and reads gate status, over stdio
// and over Streamable HTTP, against the production backend and the real
// services behind it. Every earlier MCP test used a fake backend, so not one
// of these operations had ever been seen to succeed.
func TestMCPServerOperations(t *testing.T) {
	f := newOperationsFixture(t)
	token := f.org.Name + ":" + f.owner.Session

	t.Run("streamable_http", func(t *testing.T) {
		// NovaForge's own MCP client, which agents use for external servers,
		// is the external client here: one conformant end talking to the other.
		c := mcp.NewClient(mcp.ServerDef{Name: "novaforge", URL: f.p.MCPURL, Token: token}, []string{"novaforge"})
		ctx := context.Background()
		if err := c.Connect(ctx); err != nil {
			t.Fatalf("Connect: %v", err)
		}
		tools, err := c.ListTools(ctx)
		if err != nil || len(tools) != 7 {
			t.Fatalf("ListTools = %d tools, %v", len(tools), err)
		}
		for _, cl := range f.calls("mcp/http") {
			args, _ := json.Marshal(cl.args)
			res, err := c.Call(ctx, cl.tool, args)
			if err != nil {
				t.Fatalf("%s over Streamable HTTP: %v", cl.tool, err)
			}
			for _, w := range cl.want {
				if !strings.Contains(res.Content, w) {
					t.Errorf("%s over Streamable HTTP: result lacks %q:\n%s", cl.tool, w, res.Content)
				}
			}
		}
		f.assertBranch(t, "mcp/http")

		// Another organization's name in a tool call is refused, though the
		// credential is valid.
		other := f.p.NewOrg(t, f.owner, "mcpother")
		args, _ := json.Marshal(map[string]any{"org": other.Name, "repo": f.repo.Name, "key": f.key})
		if _, err := c.Call(ctx, "novaforge.get_work_item", args); err == nil || !strings.Contains(err.Error(), "cross-org") {
			t.Fatalf("a call naming another organization: %v", err)
		}
	})

	t.Run("stdio", func(t *testing.T) {
		srv := mcp.NewServer(mcp.NewPlatformBackend(f.p.MCPClients()))
		srv.SetToken(token)
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		done := make(chan error, 1)
		go func() { done <- srv.ServeStdio(context.Background(), inR, outW); _ = outW.Close() }()
		enc := json.NewEncoder(inW)
		dec := bufio.NewScanner(outR)
		dec.Buffer(make([]byte, 0, 1<<20), 8<<20)
		id := 0
		rpc := func(method string, params any) mcp.Response {
			t.Helper()
			id++
			raw, _ := json.Marshal(params)
			if err := enc.Encode(mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(strings.TrimSpace(jsonInt(id))), Method: method, Params: raw}); err != nil {
				t.Fatalf("write %s: %v", method, err)
			}
			if !dec.Scan() {
				t.Fatalf("no response to %s: %v", method, dec.Err())
			}
			var resp mcp.Response
			if err := json.Unmarshal(dec.Bytes(), &resp); err != nil {
				t.Fatalf("decode %s: %v", method, err)
			}
			return resp
		}
		init := rpc("initialize", map[string]any{"protocolVersion": mcp.ProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}})
		if init.Error != nil {
			t.Fatalf("initialize: %v", init.Error.Message)
		}
		_ = enc.Encode(mcp.Request{JSONRPC: "2.0", Method: "notifications/initialized"})
		for _, cl := range f.calls("mcp/stdio") {
			resp := rpc("tools/call", map[string]any{"name": cl.tool, "arguments": cl.args})
			if resp.Error != nil {
				t.Fatalf("%s over stdio: protocol error %s", cl.tool, resp.Error.Message)
			}
			raw, _ := json.Marshal(resp.Result)
			var res mcp.CallResult
			_ = json.Unmarshal(raw, &res)
			if res.IsError || len(res.Content) == 0 {
				t.Fatalf("%s over stdio failed: %s", cl.tool, raw)
			}
			for _, w := range cl.want {
				if !strings.Contains(res.Content[0].Text, w) {
					t.Errorf("%s over stdio: result lacks %q:\n%s", cl.tool, w, res.Content[0].Text)
				}
			}
		}
		_ = inW.Close()
		if err := <-done; err != nil {
			t.Fatalf("ServeStdio: %v", err)
		}
		f.assertBranch(t, "mcp/stdio")
	})
}

func jsonInt(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
