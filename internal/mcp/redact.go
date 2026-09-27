package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Retain rotated credentials for the bounded lifetime of this run's client:
// a server may echo a previous request's credential in a later response.
func (c *Client) rememberCredential(token string) {
	if token == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, old := range c.credentials {
		if old == token {
			return
		}
	}
	c.credentials = append(c.credentials, token)
}
func (c *Client) redactText(text string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, token := range c.credentials {
		text = strings.ReplaceAll(text, token, "[REDACTED]")
	}
	return text
}

// Decode before redacting to handle JSON escapes and preserve numeric precision.
// Discovery schemas and RPC errors have the same trust boundary as tool output.
func (c *Client) redactJSON(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	var walk func(any) any
	walk = func(v any) any {
		switch v := v.(type) {
		case string:
			return c.redactText(v)
		case []any:
			for i := range v {
				v[i] = walk(v[i])
			}
			return v
		case map[string]any:
			safe := make(map[string]any, len(v))
			for key, val := range v {
				safe[c.redactText(key)] = walk(val)
			}
			return safe
		default:
			return v
		}
	}
	return json.Marshal(walk(value))
}
