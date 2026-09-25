package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// NewStdioClient speaks MCP over a transport the caller has already confined.
// It never starts a process: agent-runtime must supply workspace exec pipes,
// not execute an organization's command with the service's own credentials.
func NewStdioClient(def ServerDef, declared []string, stream io.ReadWriteCloser) *Client {
	c := NewClient(def, declared)
	c.stdio = stream
	c.scanner = bufio.NewScanner(stream)
	c.scanner.Buffer(make([]byte, 64*1024), maxExternalResponse)
	return c
}

// Close releases a run's external session. Stdio transport closure detaches
// exec; namespace teardown is the authority that removes sandbox processes.
func (c *Client) Close() error {
	if c.stdio != nil {
		return c.stdio.Close()
	}
	c.http.CloseIdleConnections()
	return nil
}

// stdioMessage serializes request/reply exchanges on one process. The MCP
// revision uses newline-framed JSON, not the obsolete Content-Length framing.
// Cancellation closes the stream: a late response must never satisfy the
// next request, and a process that ignores cancellation cannot pin the host.
func (c *Client) stdioMessage(ctx context.Context, method string, id json.RawMessage, params any) (rawResponse, error) {
	c.rpcMu.Lock()
	defer c.rpcMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, c.def.Timeout)
	defer cancel()
	type outcome struct {
		msg rawResponse
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		raw, err := json.Marshal(params)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		err = json.NewEncoder(c.stdio).Encode(Request{JSONRPC: "2.0", ID: id, Method: method, Params: raw})
		if err != nil || len(id) == 0 {
			done <- outcome{err: err}
			return
		}
		for c.scanner.Scan() {
			var msg rawResponse
			if err := json.Unmarshal(c.scanner.Bytes(), &msg); err != nil {
				done <- outcome{err: fmt.Errorf("invalid stdio MCP response")}
				return
			}
			if msg.Method != "" {
				continue
			} // unsolicited data is not a response or instructions
			if strings.TrimSpace(string(msg.ID)) != string(id) {
				done <- outcome{err: fmt.Errorf("stdio response does not match request %s", id)}
				return
			}
			done <- outcome{msg: msg}
			return
		}
		err = c.scanner.Err()
		if err == nil {
			err = io.EOF
		}
		done <- outcome{err: err}
	}()
	select {
	case result := <-done:
		return result.msg, result.err
	case <-ctx.Done():
		_ = c.stdio.Close()
		<-done
		return rawResponse{}, ctx.Err()
	}
}

func (c *Client) stdioRequest(ctx context.Context, method string, id int64, params, out any) error {
	msg, err := c.stdioMessage(ctx, method, json.RawMessage(strconv.FormatInt(id, 10)), params)
	if err != nil {
		return fmt.Errorf("%s on MCP server %q: %w", method, c.def.Name, err)
	}
	if msg.Error != nil {
		return fmt.Errorf("mcp server %q: %s", c.def.Name, msg.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(msg.Result, out)
}
