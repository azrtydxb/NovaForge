package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ServerDef is one external MCP server an agent may be offered.
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

// Client calls one approved external MCP server over the Streamable HTTP
// transport of the 2025-06-18 revision.
//
// The first version of this client posted a bare JSON-RPC body and decoded a
// JSON reply, which a conformant server is free to refuse: the transport
// requires the client to accept an SSE stream as the reply, to carry the
// session a server assigns, to name the negotiated protocol version on every
// later request, and to send notifications/initialized before anything else.
// A client that skips those works against its own server and no one else's.
type Client struct {
	def      ServerDef
	declared []string
	http     *http.Client

	mu      sync.Mutex
	session string
	nextID  int64
	ready   bool
}

// DefaultExternalTimeout bounds every external call, so a hung third-party
// server cannot pin an agent run until its wall-clock budget expires.
const DefaultExternalTimeout = 30 * time.Second

// maxExternalResponse bounds what one external reply may make this process
// read. The reply is untrusted, and a hostile server streaming forever must
// cost a bounded amount of memory, not the service's.
const maxExternalResponse = 4 << 20

// NewClient returns a client for def. declared is the set of server names this
// caller permits — the organization's approved servers; anything else is
// refused.
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

// Name is the server's registered name.
func (c *Client) Name() string { return c.def.Name }

// permitted reports whether the caller permits this server. It is checked on
// every call, not only on Connect, so a client built around a Connect failure
// cannot still reach a server it was never permitted to.
func (c *Client) permitted() error {
	if !slices.Contains(c.declared, c.def.Name) {
		return fmt.Errorf("mcp server %q is not declared in .novaforge/mcp/ or approved for this organization", c.def.Name)
	}
	return nil
}

// Connect verifies the server is permitted and performs the initialization
// handshake: initialize, then notifications/initialized.
func (c *Client) Connect(ctx context.Context) error {
	if err := c.permitted(); err != nil {
		return err
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "novaforge", "version": "0.1.0"},
	}, &init); err != nil {
		return fmt.Errorf("connect to mcp server %q: %w", c.def.Name, err)
	}
	// This client speaks one revision. A server that answers with another is
	// one this client cannot be sure it understands, so it disconnects rather
	// than guessing — the same rule the server applies.
	if init.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("mcp server %q speaks protocol %q; only %s is supported", c.def.Name, init.ProtocolVersion, ProtocolVersion)
	}
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
	if err := c.notify(ctx, "notifications/initialized"); err != nil {
		return fmt.Errorf("connect to mcp server %q: %w", c.def.Name, err)
	}
	return nil
}

// ListTools returns every tool the external server advertises, following its
// pagination cursor.
func (c *Client) ListTools(ctx context.Context) ([]ToolDef, error) {
	if err := c.permitted(); err != nil {
		return nil, err
	}
	var all []ToolDef
	cursor := ""
	for page := 0; page < 100; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var out struct {
			Tools      []ToolDef `json:"tools"`
			NextCursor string    `json:"nextCursor"`
		}
		if err := c.request(ctx, "tools/list", params, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Tools...)
		if out.NextCursor == "" {
			return all, nil
		}
		cursor = out.NextCursor
	}
	return nil, fmt.Errorf("mcp server %q paged its tool list past 100 pages", c.def.Name)
}

// Call invokes one tool. The returned content is always marked untrusted, and
// a result the server flags isError is returned as an error carrying that
// content, so the caller cannot mistake a failed call for a successful one.
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
	if args == nil {
		args = map[string]any{}
	}
	var raw json.RawMessage
	if err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &raw); err != nil {
		return Result{Untrusted: true}, err
	}
	var out CallResult
	if err := json.Unmarshal(raw, &out); err != nil || out.Content == nil {
		// Some servers answer with a bare value; carry it through verbatim.
		return Result{Content: string(raw), Untrusted: true}, nil
	}
	var buf bytes.Buffer
	for _, ct := range out.Content {
		buf.WriteString(ct.Text)
	}
	res := Result{Content: buf.String(), Untrusted: true}
	if out.IsError {
		return res, fmt.Errorf("mcp server %q: tool %q failed: %s", c.def.Name, name, res.Content)
	}
	return res, nil
}

// request sends one JSON-RPC request and decodes its result into out.
func (c *Client) request(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	resp, err := c.post(ctx, method, json.RawMessage(strconv.FormatInt(id, 10)), params)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	msg, err := readResponse(resp, id)
	if err != nil {
		return fmt.Errorf("%s on mcp server %q: %w", method, c.def.Name, err)
	}
	if msg.Error != nil {
		return fmt.Errorf("mcp server %q: %s", c.def.Name, msg.Error.Message)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(msg.Result, out); err != nil {
		return fmt.Errorf("decode %s from %q: %w", method, c.def.Name, err)
	}
	return nil
}

// notify sends one JSON-RPC notification, which a server acknowledges with
// 202 and no body.
func (c *Client) notify(ctx context.Context, method string) error {
	resp, err := c.post(ctx, method, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxExternalResponse))
	return nil
}

func (c *Client) post(ctx context.Context, method string, id json.RawMessage, params any) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, c.def.Timeout)
	var p json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			cancel()
			return nil, err
		}
		p = b
	}
	body, err := json.Marshal(Request{JSONRPC: "2.0", ID: id, Method: method, Params: p})
	if err != nil {
		cancel()
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.def.URL, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	c.mu.Lock()
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	if c.ready {
		req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	}
	c.mu.Unlock()
	if c.def.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.def.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%s on mcp server %q: %w", method, c.def.Name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("%s on mcp server %q: %s", method, c.def.Name, resp.Status)
	}
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		c.mu.Lock()
		c.session = s
		c.mu.Unlock()
	}
	resp.Body = cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// rawResponse is a JSON-RPC response whose result is kept undecoded until the
// caller knows its shape.
type rawResponse struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

// readResponse reads the response to request id from either a JSON body or
// an SSE stream. In a stream the server may send notifications and requests
// of its own first; only the message answering id is the response.
func readResponse(resp *http.Response, id int64) (rawResponse, error) {
	body := io.LimitReader(resp.Body, maxExternalResponse)
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	want := strconv.FormatInt(id, 10)

	if mediaType != "text/event-stream" {
		var msg rawResponse
		if err := json.NewDecoder(body).Decode(&msg); err != nil {
			return rawResponse{}, fmt.Errorf("decode response: %w", err)
		}
		return msg, nil
	}

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), maxExternalResponse)
	var data strings.Builder
	flush := func() (rawResponse, bool) {
		defer data.Reset()
		if data.Len() == 0 {
			return rawResponse{}, false
		}
		var msg rawResponse
		if err := json.Unmarshal([]byte(data.String()), &msg); err != nil {
			return rawResponse{}, false
		}
		if msg.Method != "" || strings.TrimSpace(string(msg.ID)) != want {
			return rawResponse{}, false
		}
		return msg, true
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if msg, ok := flush(); ok {
				return msg, nil
			}
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if msg, ok := flush(); ok {
		return msg, nil
	}
	if err := sc.Err(); err != nil {
		return rawResponse{}, fmt.Errorf("read event stream: %w", err)
	}
	return rawResponse{}, fmt.Errorf("the event stream ended without a response to request %s", want)
}
