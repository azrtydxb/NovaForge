package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"
)

// ServerDef is one external MCP server, as declared under .novaforge/mcp/.
type ServerDef struct {
	Name    string        `yaml:"name" json:"name"`
	URL     string        `yaml:"url" json:"url"`
	Token   string        `yaml:"token,omitempty" json:"token,omitempty"`
	Timeout time.Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// Result is one external tool call's output. Untrusted is always true: an
// external server's response is data an agent may read, never instructions the
// platform acts on.
type Result struct {
	Content   string
	Untrusted bool
}

// Client calls an approved external MCP server.
type Client struct {
	def      ServerDef
	declared []string
	http     *http.Client
	tools    []ToolDef
}

// DefaultExternalTimeout bounds every external call, so a hung third-party
// server cannot pin an agent run until its wall-clock budget expires.
const DefaultExternalTimeout = 30 * time.Second

// NewClient returns a client for def. declared is the set of server names the
// repository's .novaforge/mcp/ configuration permits; anything else is refused.
func NewClient(def ServerDef, declared []string) *Client {
	if def.Timeout <= 0 {
		def.Timeout = DefaultExternalTimeout
	}
	return &Client{
		def:      def,
		declared: declared,
		http:     &http.Client{Timeout: def.Timeout},
	}
}

// permitted reports whether the repository's configuration declares this
// server. It is checked on every call, not only on Connect, so a client built
// around a Connect failure cannot still reach an undeclared server.
func (c *Client) permitted() error {
	if !slices.Contains(c.declared, c.def.Name) {
		return fmt.Errorf("mcp server %q is not declared in .novaforge/mcp/", c.def.Name)
	}
	return nil
}

// Connect verifies the server is permitted and negotiates the protocol.
func (c *Client) Connect(ctx context.Context) error {
	if err := c.permitted(); err != nil {
		return err
	}
	var resp Response
	if err := c.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"clientInfo":      map[string]any{"name": "novaforge", "version": "0.1.0"},
	}, &resp); err != nil {
		return fmt.Errorf("connect to mcp server %q: %w", c.def.Name, err)
	}
	return nil
}

// ListTools returns the tools the external server advertises.
func (c *Client) ListTools(ctx context.Context) ([]ToolDef, error) {
	var resp Response
	if err := c.rpc(ctx, "tools/list", map[string]any{}, &resp); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode tools/list from %q: %w", c.def.Name, err)
	}
	c.tools = out.Tools
	return out.Tools, nil
}

// Call invokes one tool. The returned content is always marked untrusted.
func (c *Client) Call(ctx context.Context, name string, argsJSON []byte) (Result, error) {
	if err := c.permitted(); err != nil {
		return Result{Untrusted: true}, err
	}
	var args map[string]any
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return Result{Untrusted: true}, fmt.Errorf("encode arguments for %q: %w", name, err)
		}
	}
	var resp Response
	if err := c.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &resp); err != nil {
		return Result{Untrusted: true}, err
	}
	if resp.Error != nil {
		return Result{Untrusted: true}, fmt.Errorf("mcp server %q: %s", c.def.Name, resp.Error.Message)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return Result{Untrusted: true}, err
	}
	var out CallResult
	if err := json.Unmarshal(raw, &out); err != nil {
		// Some servers answer with a bare value; carry it through verbatim.
		return Result{Content: string(raw), Untrusted: true}, nil
	}
	var buf bytes.Buffer
	for _, ct := range out.Content {
		buf.WriteString(ct.Text)
	}
	return Result{Content: buf.String(), Untrusted: true}, nil
}

func (c *Client) rpc(ctx context.Context, method string, params any, out *Response) error {
	ctx, cancel := context.WithTimeout(ctx, c.def.Timeout)
	defer cancel()

	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	body, err := json.Marshal(Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: method, Params: p,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.def.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.def.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.def.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s on mcp server %q: %w", method, c.def.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s on mcp server %q: %s", method, c.def.Name, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
