package agentrun_test

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpv1 "github.com/novaforge/novaforge/gen/novaforge/mcp/v1"
	"github.com/novaforge/novaforge/internal/mcp"
	"google.golang.org/grpc"
)

type stdioRegister struct{ status string }

func (r stdioRegister) ListApprovedServers(context.Context, *mcpv1.ListApprovedServersRequest, ...grpc.CallOption) (*mcpv1.ListApprovedServersResponse, error) {
	state := r.status
	if state == "" {
		state = "approved"
	}
	return &mcpv1.ListApprovedServersResponse{Servers: []*mcpv1.McpServer{{Id: "server", Name: "selected", Transport: "stdio", Url: "marker", Status: state}}}, nil
}

func TestRunnerFiltersStdioBeforeStartup(t *testing.T) {
	p := newPlatform(t)
	for _, allow := range []string{"[work.get]", "[]", "[mcp.selected.lookup]", "[mcp.selected-other.lookup]", "[mcp.selected..lookup]", "[mcp.selected.*]", "[mcp.unapproved.lookup]"} {
		t.Run(allow, func(t *testing.T) {
			o := p.newOrg(t)
			repo := p.repo(t, o, map[string]string{".novaforge/agents/engineer.yaml": "name: engineer\nrole: engineer\ntools: " + allow + "\n"})
			run := p.startRun(t, o, repo, "engineer", "read", nil)
			model := &recordingModel{script: []stubResponse{{text: "done"}}}
			if allow == "[mcp.selected.lookup]" {
				model.script = []stubResponse{{toolCalls: []provider.ToolCallPart{toolCall("m1", "mcp.selected.lookup", map[string]any{})}}, {text: "done"}}
			}
			var requested []string
			runner := p.runner(model, &requested)
			runner.MCP = stdioRegister{}
			marker := filepath.Join(t.TempDir(), "started")
			runner.MCPOptions.OpenStdio = func(context.Context, string) (io.ReadWriteCloser, error) {
				if err := os.WriteFile(marker, []byte("startup side effect"), 0600); err != nil {
					return nil, err
				}
				local, remote := net.Pipe()
				go func() {
					defer remote.Close()
					scan := bufio.NewScanner(remote)
					for scan.Scan() {
						var req mcp.Request
						if json.Unmarshal(scan.Bytes(), &req) != nil {
							return
						}
						if len(req.ID) == 0 {
							continue
						}
						var res any
						switch req.Method {
						case "initialize":
							res = map[string]any{"protocolVersion": mcp.ProtocolVersion}
						case "tools/call":
							res = mcp.CallResult{Content: []mcp.Content{{Type: "text", Text: "selected tool worked"}}}
						case "tools/list":
							res = map[string]any{"tools": []mcp.ToolDef{{Name: "lookup", InputSchema: map[string]any{"type": "object"}}}}
						}
						if json.NewEncoder(remote).Encode(mcp.Response{JSONRPC: "2.0", ID: req.ID, Result: res}) != nil {
							return
						}
					}
				}()
				return &lifecycleTestStream{ReadWriteCloser: local, closeFn: func() error { return nil }}, nil
			}
			result := runner.Run(o.ctx, run, p.runtimeFor(run))
			malformed := strings.Contains(allow, "..") || strings.Contains(allow, "*")
			if malformed {
				if result.State != "failed" {
					t.Fatalf("malformed selection accepted: %+v", result)
				}
			} else if result.State != "succeeded" {
				t.Fatalf("%+v", result)
			}
			_, err := os.Stat(marker)
			if allow == "[mcp.selected.lookup]" {
				entries, e := p.audit.List(o.ctx, run.ID)
				if e != nil {
					t.Fatal(e)
				}
				worked := false
				for _, entry := range entries {
					if entry.Tool == "mcp.selected.lookup" && entry.Outcome == "ok" {
						worked = true
					}
				}
				if !worked {
					t.Fatal("selected stdio tool never succeeded")
				}
				if err != nil {
					t.Fatal("selected stdio never started")
				}
				if got := model.offeredTools(t); len(got) != 1 || got[0] != "mcp.selected.lookup" {
					t.Fatalf("tools=%v", got)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal("unselected stdio command executed before tool filtering")
			}
		})
	}
}
