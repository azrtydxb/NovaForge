package tools

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/google/uuid"
	mcpv1 "github.com/novaforge/novaforge/gen/novaforge/mcp/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/egress"
)

// OperatorMCP binds approved register entries to operator-owned credentials and
// commands. Repository configuration can narrow offered tools, never invent these
// bindings. The existing NF_MCP_HTTP_CONFIG_FILE mount also owns stdio policy.
type OperatorMCP struct {
	path     string
	Bindings []MCPBinding `json:"bindings"`
}
type MCPBinding struct {
	OrgID     uuid.UUID `json:"org_id"`
	RepoID    uuid.UUID `json:"repo_id"`
	ServerID  uuid.UUID `json:"server_id"`
	URL       string    `json:"url"`
	Transport string    `json:"transport"`
	Public    bool      `json:"public,omitempty"`
	CAFile    string    `json:"ca_file,omitempty"`
	TokenFile string    `json:"token_file,omitempty"`
	CIDRs     []string  `json:"cidrs,omitempty"`
	Command   []string  `json:"command,omitempty"`
}

var errMCPPolicy = errors.New("MCP operator policy or credential unavailable")

func readMCPFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errMCPPolicy
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errMCPPolicy
	}
	return b, nil
}
func LoadOperatorMCP(path string) (*OperatorMCP, error) {
	c := &OperatorMCP{path: path}
	if path == "" {
		return c, nil
	}
	raw, err := readMCPFile(path, 1<<20)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(c) != nil || d.Decode(new(any)) != io.EOF {
		return nil, errMCPPolicy
	}
	seen := map[[3]uuid.UUID]bool{}
	for _, b := range c.Bindings {
		key := [3]uuid.UUID{b.OrgID, b.RepoID, b.ServerID}
		if b.OrgID == uuid.Nil || b.RepoID == uuid.Nil || b.ServerID == uuid.Nil || seen[key] || b.URL == "" {
			return nil, errMCPPolicy
		}
		seen[key] = true
		if b.CAFile != "" && (filepath.Base(b.CAFile) != b.CAFile || b.CAFile == "." || b.CAFile == "..") {
			return nil, errMCPPolicy
		}
		switch b.Transport {
		case "streamable_http":
			u, e := url.Parse(b.URL)
			if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || len(b.Command) != 0 || b.Public == (b.TokenFile != "") {
				return nil, errMCPPolicy
			}
			if b.TokenFile != "" && (u.Scheme != "https" || filepath.Base(b.TokenFile) != b.TokenFile || b.TokenFile == "." || b.TokenFile == "..") {
				return nil, errMCPPolicy
			}
			if _, e = b.policy(); e != nil {
				return nil, errMCPPolicy
			}
		case "stdio":
			if len(b.Command) == 0 || len(b.Command) > 64 || !filepath.IsAbs(b.Command[0]) || b.TokenFile != "" || b.CAFile != "" || b.Public || len(b.CIDRs) > 0 {
				return nil, errMCPPolicy
			}
			for _, arg := range b.Command {
				if len(arg) > 16384 || strings.ContainsRune(arg, 0) {
					return nil, errMCPPolicy
				}
			}
		default:
			return nil, errMCPPolicy
		}
	}
	return c, nil
}
func (b MCPBinding) policy() (*egress.Policy, error) {
	u, e := url.Parse(b.URL)
	if e != nil {
		return nil, e
	}
	port := 443
	if u.Scheme == "http" {
		port = 80
	}
	if u.Port() != "" {
		port, e = strconv.Atoi(u.Port())
		if e != nil {
			return nil, e
		}
	}
	raw, _ := json.Marshal([]egress.Rule{{Scheme: u.Scheme, Host: u.Hostname(), Port: port, CIDRs: b.CIDRs}})
	return egress.Parse(string(raw))
}
func (c *OperatorMCP) binding(ctx context.Context, org, repo uuid.UUID, s *mcpv1.McpServer) (MCPBinding, error) {
	if authz.RequireOrg(ctx, org) != nil || s.GetOrgId() != org.String() {
		return MCPBinding{}, errMCPPolicy
	}
	for _, b := range c.Bindings {
		if b.OrgID == org && b.RepoID == repo && b.ServerID.String() == s.GetId() && b.URL == s.GetUrl() && b.Transport == s.GetTransport() {
			return b, nil
		}
	}
	return MCPBinding{}, errMCPPolicy
}

// Options snapshots binding identity but rechecks mounted policy on every call.
// Token bytes are read anew for every HTTP request, never put in argv or logs.
func (c *OperatorMCP) Options(org, repo uuid.UUID, open func(context.Context, []string) (io.ReadWriteCloser, error)) MCPOptions {
	check := func(ctx context.Context, s *mcpv1.McpServer) error {
		old, e := c.binding(ctx, org, repo, s)
		if e != nil {
			return e
		}
		now, e := LoadOperatorMCP(c.path)
		if e != nil {
			return e
		}
		current, e := now.binding(ctx, org, repo, s)
		if e != nil || !reflect.DeepEqual(old, current) {
			return errMCPPolicy
		}
		return nil
	}
	return MCPOptions{
		Revalidate: check,
		OpenApprovedStdio: func(ctx context.Context, s *mcpv1.McpServer) (io.ReadWriteCloser, error) {
			if check(ctx, s) != nil || open == nil {
				return nil, errMCPPolicy
			}
			b, e := c.binding(ctx, org, repo, s)
			if e != nil {
				return nil, e
			}
			return open(ctx, append([]string(nil), b.Command...))
		},
		HTTPAuthorization: func(ctx context.Context, s *mcpv1.McpServer) (MCPAuthorization, error) {
			if err := check(ctx, s); err != nil {
				return MCPAuthorization{}, err
			}
			b, e := c.binding(ctx, org, repo, s)
			if e != nil {
				return MCPAuthorization{}, e
			}
			p, e := b.policy()
			if e != nil {
				return MCPAuthorization{}, errMCPPolicy
			}
			var tlsConfig *tls.Config
			if b.CAFile != "" {
				raw, err := readMCPFile(filepath.Join(filepath.Dir(c.path), b.CAFile), 65536)
				roots := x509.NewCertPool()
				if err != nil || !roots.AppendCertsFromPEM(raw) {
					return MCPAuthorization{}, errMCPPolicy
				}
				tlsConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
			}
			a := MCPAuthorization{Public: b.Public, Client: p.ClientWithTLS(tlsConfig), Check: func(call context.Context) error { return check(call, s) }}
			if !b.Public {
				a.BearerToken = func(call context.Context) (string, error) {
					if e := check(call, s); e != nil {
						return "", e
					}
					raw, e := readMCPFile(filepath.Join(filepath.Dir(c.path), b.TokenFile), 16384)
					token := strings.TrimSpace(string(raw))
					if e != nil || token == "" || strings.ContainsAny(token, "\r\n") {
						return "", errMCPPolicy
					}
					return token, nil
				}
			}
			return a, nil
		},
	}
}
