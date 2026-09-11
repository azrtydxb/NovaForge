// Package mcp implements NovaForge's own MCP server, so external agents such
// as Claude Code and Codex can drive the platform, plus a client for approved
// external MCP servers.
//
// It conforms to the current MCP specification revision only. There is
// deliberately no code path for the deprecated HTTP+SSE transport: stdio and
// Streamable HTTP are the whole surface.
package mcp

import "encoding/json"

// ProtocolVersion is the MCP specification revision this server implements.
// Only this revision is offered; there is no legacy negotiation.
const ProtocolVersion = "2025-06-18"

// Request is a JSON-RPC 2.0 request or notification. A notification has no id.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// JSON-RPC 2.0 reserved error codes, plus the application code used when a
// caller presents no usable credential.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeUnauthorized   = -32001
)

func newResponse(id json.RawMessage, result any) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Result: result}
}

func newError(id json.RawMessage, code int, msg string) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}}
}

// ToolDef describes one tool in a tools/list result.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Content is one item of a tools/call result.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CallResult is the result of tools/call.
type CallResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}
