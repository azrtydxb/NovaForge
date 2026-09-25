package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestConfinedStdioClient(t *testing.T) {
	local, remote := net.Pipe()
	defer remote.Close()
	go func() {
		sc := bufio.NewScanner(remote)
		for sc.Scan() {
			var req Request
			if json.Unmarshal(sc.Bytes(), &req) != nil {
				return
			}
			if len(req.ID) == 0 {
				continue
			}
			var result any
			switch req.Method {
			case "initialize":
				result = map[string]any{"protocolVersion": ProtocolVersion}
			case "tools/list":
				result = map[string]any{"tools": []ToolDef{{Name: "lookup", InputSchema: map[string]any{"type": "object"}}}}
			case "tools/call":
				result = CallResult{Content: []Content{{Type: "text", Text: "untrusted"}}}
			}
			if json.NewEncoder(remote).Encode(Response{JSONRPC: "2.0", ID: req.ID, Result: result}) != nil {
				return
			}
		}
	}()
	c := NewStdioClient(ServerDef{Name: "local"}, []string{"local"}, local)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := c.ListTools(ctx)
	if err != nil || len(tools) != 1 {
		t.Fatalf("tools=%v err=%v", tools, err)
	}
	result, err := c.Call(ctx, "lookup", []byte(`{}`))
	if err != nil || result.Content != "untrusted" || !result.Untrusted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
func TestStdioTimeoutClosesTransport(t *testing.T) {
	local, remote := net.Pipe()
	defer remote.Close()
	c := NewStdioClient(ServerDef{Name: "hung", Timeout: 20 * time.Millisecond}, []string{"hung"}, local)
	defer c.Close()
	if err := c.Connect(context.Background()); err == nil {
		t.Fatal("hung stdio connected")
	}
	if _, err := remote.Write([]byte("x")); err == nil {
		t.Fatal("timed-out transport remained open")
	}
}
