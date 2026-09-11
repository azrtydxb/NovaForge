// Package cli implements nf, the command-line client for NovaForge. While the
// web GUI is deferred, nf is how a human drives the platform, so it must cover
// the whole REST surface rather than a convenience subset.
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is the persisted CLI state. It holds a bearer token, so the file it
// lives in is written 0600 and never world-readable.
type Config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
	Org    string `json:"org,omitempty"`
}

func configPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate home directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "novaforge", "config.json"), nil
}

// SaveConfig writes the config with 0600 permissions.
func SaveConfig(c Config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	// WriteFile does not narrow an existing file's mode, and this one holds a
	// token, so the mode is enforced explicitly.
	if err := os.Chmod(p, 0o600); err != nil {
		return fmt.Errorf("chmod config: %w", err)
	}
	return nil
}

// LoadConfig reads the persisted config. A missing file yields a zero Config.
func LoadConfig() (Config, error) {
	p, err := configPath()
	if err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", p, err)
	}
	return c, nil
}

// Client talks to the NovaForge REST edge.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient returns a client for the edge at base, authenticating with token.
func NewClient(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 60 * time.Second},
	}
}

// Do performs one request. body, when non-nil, is JSON-encoded; out, when
// non-nil, receives the decoded response. A non-2xx status becomes an error
// carrying the status and the server's message, because a CLI that swallows
// the reason is useless at 3am.
func (c *Client) Do(method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode response from %s %s: %w", method, path, err)
		}
	}
	return nil
}
