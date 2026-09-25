package semanticindex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This is a serial, one-shot definition client, not a general LSP framework.
// Byte and packet ceilings include unsolicited server traffic, so a notification
// flood or giant header cannot bypass the response bound.
type lspConnection struct {
	ctx      context.Context
	reader   *bufio.Reader
	writer   io.Writer
	nextID   int
	bytes    int
	messages int
}

type lspMessage struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *lspConnection) context() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

func (c *lspConnection) send(message any) error {
	if err := c.context().Err(); err != nil {
		return err
	}
	b, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.bytes += len(b)
	if c.bytes > MaxArtifactBytes {
		return fmt.Errorf("LSP session byte limit exceeded")
	}
	packet := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(b), b)
	n, err := io.WriteString(c.writer, packet)
	if err == nil && n != len(packet) {
		err = io.ErrShortWrite
	}
	return err
}

func (c *lspConnection) notify(method string, params any) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *lspConnection) call(method string, params any) (json.RawMessage, error) {
	c.nextID++
	id := strconv.Itoa(c.nextID)
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		message, err := c.receive()
		if err != nil {
			return nil, err
		}
		if message.Method != "" {
			if len(message.ID) == 0 {
				continue // diagnostics and progress are not evidence of completeness
			}
			// No dynamic registration, applyEdit, commands or repository-provided
			// configuration are accepted. Configuration queries get null defaults.
			response := map[string]any{"jsonrpc": "2.0", "id": message.ID}
			if message.Method == "workspace/configuration" {
				var p struct {
					Items json.RawMessage `json:"items"`
				}
				if err := json.Unmarshal(message.Params, &p); err != nil {
					return nil, fmt.Errorf("invalid LSP configuration request")
				}
				items, err := decodeJSONArray(c.context(), p.Items, MaxQueries)
				if err != nil {
					return nil, err
				}
				response["result"] = make([]any, len(items))
			} else {
				response["error"] = map[string]any{"code": -32601, "message": "client method not supported"}
			}
			if err := c.send(response); err != nil {
				return nil, err
			}
			continue
		}
		if string(message.ID) != id {
			return nil, fmt.Errorf("unexpected LSP response ID")
		}
		if message.Error != nil {
			return nil, fmt.Errorf("LSP server returned error code %d", message.Error.Code)
		}
		if len(message.Result) == 0 {
			return nil, fmt.Errorf("LSP response has no result")
		}
		return message.Result, nil
	}
}

func (c *lspConnection) receive() (lspMessage, error) {
	var message lspMessage
	c.messages++
	if c.messages > 10000 {
		return message, fmt.Errorf("LSP message limit exceeded")
	}
	length, headerBytes := -1, 0
	for {
		line, err := c.reader.ReadSlice('\n')
		if err != nil {
			return message, fmt.Errorf("LSP header: %w", err)
		}
		headerBytes += len(line)
		if headerBytes > 8192 || !strings.HasSuffix(string(line), "\r\n") {
			return message, fmt.Errorf("invalid or oversized LSP header")
		}
		if string(line) == "\r\n" {
			break
		}
		key, value, ok := strings.Cut(strings.TrimSuffix(string(line), "\r\n"), ":")
		if !ok {
			return message, fmt.Errorf("invalid LSP header field")
		}
		if strings.EqualFold(key, "Content-Length") {
			if length != -1 {
				return message, fmt.Errorf("duplicate LSP Content-Length")
			}
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil || length < 0 || length > MaxArtifactBytes {
				return message, fmt.Errorf("invalid LSP Content-Length")
			}
		}
	}
	if length < 0 || length > MaxArtifactBytes-c.bytes-headerBytes {
		return message, fmt.Errorf("LSP missing length or session byte limit exceeded")
	}
	c.bytes += length + headerBytes
	b := make([]byte, length)
	if _, err := io.ReadFull(c.reader, b); err != nil {
		return message, err
	}
	if err := validateJSONStrings(c.context(), b); err != nil {
		return message, err
	}
	if err := json.Unmarshal(b, &message); err != nil {
		return message, err
	}
	if message.Version != "2.0" {
		return message, fmt.Errorf("invalid LSP JSON-RPC version")
	}
	return message, nil
}

// encoding/json repairs malformed Unicode. Evidence identities must instead
// preserve the producer's exact string, including legitimate U+FFFD characters.
// Syntax validation remains encoding/json's job; this scan rejects its lossy
// Unicode cases before any string can become an identity.
func validateJSONStrings(ctx context.Context, b []byte) error {
	quoted := false
	nextCheck := 0
	hex4 := func(b []byte) (uint16, bool) {
		if len(b) < 4 {
			return 0, false
		}
		var n uint16
		for _, c := range b[:4] {
			n <<= 4
			switch {
			case c >= '0' && c <= '9':
				n += uint16(c - '0')
			case c >= 'a' && c <= 'f':
				n += uint16(c - 'a' + 10)
			case c >= 'A' && c <= 'F':
				n += uint16(c - 'A' + 10)
			default:
				return 0, false
			}
		}
		return n, true
	}
	for i := 0; i < len(b); i++ {
		if i >= nextCheck {
			if err := ctx.Err(); err != nil {
				return err
			}
			nextCheck = i + 4096
		}
		if b[i] >= utf8.RuneSelf {
			_, size := utf8.DecodeRune(b[i:])
			if size == 1 {
				return fmt.Errorf("invalid UTF-8 in JSON")
			}
			i += size - 1
			continue
		}
		if b[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return fmt.Errorf("unfinished JSON escape")
		}
		if b[i] != 'u' {
			continue
		}
		n, ok := hex4(b[i+1:])
		if !ok {
			return fmt.Errorf("invalid JSON Unicode escape")
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return fmt.Errorf("unpaired JSON low surrogate")
		}
		if n >= 0xd800 && n <= 0xdbff {
			if len(b)-i < 7 || b[i+1] != '\\' || b[i+2] != 'u' {
				return fmt.Errorf("unpaired JSON high surrogate")
			}
			low, ok := hex4(b[i+3:])
			if !ok || low < 0xdc00 || low > 0xdfff {
				return fmt.Errorf("unpaired JSON high surrogate")
			}
			i += 6
		}
	}
	return ctx.Err()
}

// Keep at most limit raw elements. In particular, do not unmarshal a whole
// array and check len afterwards: small wire elements can amplify allocations.
func decodeJSONArray(ctx context.Context, data []byte, limit int) ([]json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, nil
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	if token != json.Delim('[') {
		return nil, fmt.Errorf("expected JSON array")
	}
	var items []json.RawMessage
	for d.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(items) >= limit {
			return nil, fmt.Errorf("JSON collection limit exceeded (%d)", limit)
		}
		var item json.RawMessage
		if err := d.Decode(&item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON array data")
	}
	return items, ctx.Err()
}
