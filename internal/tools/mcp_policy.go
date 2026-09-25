package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	mcpv1 "github.com/novaforge/novaforge/gen/novaforge/mcp/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// MCPHTTPPolicy is operator configuration, never repository or model input.
// BearerTokenFile supplies STATIC authentication, not a brokered expiring lease.
type MCPHTTPPolicy struct {
	OrgID           string `json:"org_id"`
	ServerID        string `json:"server_id"`
	URL             string `json:"url"`
	Public          bool   `json:"public"`
	BearerTokenFile string `json:"bearer_token_file"`
}

// UnmarshalJSON rejects duplicate and case-folded fields. encoding/json's
// default last-value-wins behavior is ambiguous for operator authority.
func (p *MCPHTTPPolicy) UnmarshalJSON(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("MCP HTTP policy must be an object")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return fmt.Errorf("duplicate MCP HTTP policy field")
		}
		seen[key] = true
		switch key {
		case "org_id", "server_id", "url", "public", "bearer_token_file":
		default:
			return fmt.Errorf("unknown MCP HTTP policy field")
		}
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			return err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("null MCP HTTP policy field")
		}
	}
	// Decode through an alias to retain standard string/boolean type checks.
	type plain MCPHTTPPolicy
	var policy plain
	if err = json.Unmarshal(body, &policy); err != nil {
		return err
	}
	*p = MCPHTTPPolicy(policy)
	return nil
}

// LoadMCPHTTPPolicies reads an immutable destination map once at startup. Only
// token files rotate per request. Empty configuration offers no HTTP authority.
func LoadMCPHTTPPolicies(path string) (func(context.Context, *mcpv1.McpServer) (MCPAuthorization, error), error) {
	policies := map[string]MCPHTTPPolicy{}
	if path != "" {
		body, err := readBoundedFile(path, 1<<20)
		if err != nil {
			return nil, fmt.Errorf("MCP HTTP policy file unavailable")
		}
		var list []MCPHTTPPolicy
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&list); err != nil {
			return nil, fmt.Errorf("invalid MCP HTTP policy file")
		}
		if err = decoder.Decode(new(any)); err != io.EOF {
			return nil, fmt.Errorf("MCP HTTP policy has trailing content")
		}
		for _, p := range list {
			org, e := uuid.Parse(p.OrgID)
			if e != nil || org == uuid.Nil {
				return nil, fmt.Errorf("MCP HTTP policy has invalid organization")
			}
			server, e := uuid.Parse(p.ServerID)
			if e != nil || server == uuid.Nil {
				return nil, fmt.Errorf("MCP HTTP policy has invalid server")
			}
			if _, e = policyDestination(p.URL); e != nil {
				return nil, e
			}
			if p.Public == (p.BearerTokenFile != "") {
				return nil, fmt.Errorf("MCP HTTP policy requires exactly public or static bearer file")
			}
			if p.BearerTokenFile != "" && (!filepath.IsAbs(p.BearerTokenFile) || filepath.Clean(p.BearerTokenFile) != p.BearerTokenFile) {
				return nil, fmt.Errorf("MCP bearer file must be an absolute operator path")
			}
			p.OrgID = org.String()
			p.ServerID = server.String()
			key := p.OrgID + "/" + p.ServerID
			if _, exists := policies[key]; exists {
				return nil, fmt.Errorf("duplicate MCP HTTP policy")
			}
			policies[key] = p
		}
	}
	return func(ctx context.Context, server *mcpv1.McpServer) (MCPAuthorization, error) {
		scope, err := authz.FromContext(ctx)
		if err != nil || scope.OrgID == uuid.Nil || server == nil || server.GetOrgId() != scope.OrgID.String() || server.GetStatus() != "approved" || server.GetTransport() != "streamable_http" {
			return MCPAuthorization{}, fmt.Errorf("MCP HTTP authorization unavailable")
		}
		p, ok := policies[scope.OrgID.String()+"/"+server.GetId()]
		// Exact text equality intentionally refuses aliases/redirects. Validation
		// rejects ambiguous URL forms rather than normalizing credentials onto them.
		if !ok || p.URL != server.GetUrl() {
			return MCPAuthorization{}, fmt.Errorf("MCP HTTP destination has no operator policy")
		}
		if p.Public {
			return MCPAuthorization{Public: true}, nil
		}
		return MCPAuthorization{BearerToken: func(ctx context.Context) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			raw, err := readBoundedFile(p.BearerTokenFile, 64<<10)
			if err != nil {
				return "", fmt.Errorf("MCP static bearer file unavailable")
			}
			token := strings.TrimSpace(string(raw))
			if token == "" || strings.ContainsAny(token, "\r\n\t ") {
				return "", fmt.Errorf("MCP static bearer file invalid")
			}
			return token, nil
		}}, nil
	}, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return nil, fmt.Errorf("regular file required")
	}
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("file too large")
	}
	return body, nil
}

func policyDestination(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Opaque != "" || u.String() != raw || strings.ContainsAny(raw, "\r\n\t ") {
		return nil, fmt.Errorf("invalid MCP HTTP policy destination")
	}
	host := strings.ToLower(u.Hostname())
	if host != u.Hostname() {
		return nil, fmt.Errorf("MCP policy host must be canonical lowercase")
	}
	if u.Scheme == "https" {
		return u, nil
	}
	// Plain HTTP is allowed only for an exact operator-approved local/cluster
	// destination. A hostname merely resolving to a private IP is insufficient.
	ip := net.ParseIP(host)
	private := ip != nil && (ip.IsLoopback() || ip.IsPrivate())
	if u.Scheme == "http" && (private || strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".svc.cluster.local")) {
		return u, nil
	}
	return nil, fmt.Errorf("MCP HTTP policy requires HTTPS outside the private cluster")
}
