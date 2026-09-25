package secrets

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OpenBaoBinding is operator configuration, never supplied by a job. Path
// names an approved dynamic engine role. Exactly one mapping must be set:
// JSON preserves the complete credential object; Field returns a string field
// (for example a provider-issued kubeconfig). Neither fabricates credentials.
type OpenBaoBinding struct {
	OrgID       uuid.UUID         `json:"org_id"`
	Environment string            `json:"environment"`
	Name        string            `json:"name"`
	Path        string            `json:"path"`
	Method      string            `json:"method"`
	Parameters  map[string]string `json:"parameters,omitempty"`
	Field       string            `json:"field,omitempty"`
	JSON        bool              `json:"json,omitempty"`
	// RequireHardExpiry refuses engines without independently verified target-expiry evidence.
	RequireHardExpiry bool `json:"require_hard_expiry,omitempty"`
}

type OpenBaoConfig struct {
	Endpoint  string           `json:"endpoint"`
	TokenFile string           `json:"token_file"`
	CAFile    string           `json:"ca_file,omitempty"`
	Bindings  []OpenBaoBinding `json:"bindings"`
}

type bindingKey struct {
	org               uuid.UUID
	environment, name string
}
type openBao struct {
	endpoint, tokenFile string
	client              *http.Client
	bindings            map[bindingKey]OpenBaoBinding
}

var ErrHardExpiryUnavailable = errors.New("target-enforced hard expiry is not verified for this OpenBao binding")

var ErrProviderUnavailable = errors.New("OpenBao provider unavailable")
var ErrProviderNotConfigured = errors.New("no dynamic credential provider binding configured; static values cannot be issued as expiring credentials")
var ErrProviderContract = errors.New("OpenBao dynamic credential contract rejected")

// NewOpenBaoBroker copies bindings at startup. Request-time callers cannot
// change a path or choose another organization's or environment's role.
func NewOpenBaoBroker(pool *pgxpool.Pool, kek []byte, cfg OpenBaoConfig) (*Broker, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid OpenBao endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return nil, errors.New("OpenBao requires HTTPS (HTTP is allowed only on loopback for isolated tests)")
	}
	if cfg.TokenFile == "" {
		return nil, errors.New("OpenBao token file is required")
	}
	p := &openBao{endpoint: strings.TrimRight(cfg.Endpoint, "/"), tokenFile: cfg.TokenFile, bindings: make(map[bindingKey]OpenBaoBinding), client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("OpenBao redirects forbidden") }}}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, errors.New("OpenBao CA file cannot be read")
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("OpenBao CA file has no certificates")
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		p.client.Transport = transport
	}
	for _, v := range cfg.Bindings {
		if v.OrgID == uuid.Nil || !ValidName(v.Name) || (v.Environment != EnvironmentStaging && v.Environment != EnvironmentProduction) || !validBaoPath(v.Path) || (v.JSON == (v.Field != "")) || (v.Method != "GET" && v.Method != "POST") || (v.Method == "GET" && len(v.Parameters) > 0) {
			return nil, errors.New("invalid OpenBao binding")
		}
		k := bindingKey{v.OrgID, v.Environment, v.Name}
		if _, exists := p.bindings[k]; exists {
			return nil, errors.New("duplicate OpenBao binding")
		}
		params := make(map[string]string, len(v.Parameters))
		for k, val := range v.Parameters {
			params[k] = val
		}
		v.Parameters = params
		p.bindings[k] = v
	}
	b := NewBroker(pool, kek)
	b.provider = p
	return b, nil
}

func validBaoPath(s string) bool {
	if s == "" || path.Clean(s) != s || strings.HasPrefix(s, "/") || strings.ContainsAny(s, "?%#\\") {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "." || part == ".." {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}

type baoResponse struct {
	LeaseID       string          `json:"lease_id"`
	LeaseDuration int64           `json:"lease_duration"`
	Data          json.RawMessage `json:"data"`
}

// Errors never include provider bodies, headers, URLs or data: engine errors
// can echo credentials. Read the mounted token on each call to allow rotation.
func (p *openBao) call(ctx context.Context, method, route string, body any, out any) error {
	token, err := os.ReadFile(p.tokenFile)
	if err != nil || strings.TrimSpace(string(token)) == "" {
		return ErrProviderUnavailable
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return ErrProviderContract
	}
	req, err := http.NewRequestWithContext(ctx, method, p.endpoint+"/v1/"+route, bytes.NewReader(payload))
	if err != nil {
		return ErrProviderContract
	}
	req.Header.Set("X-Vault-Token", strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return ErrProviderUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: HTTP %d", ErrProviderContract, resp.StatusCode)
		}
		return fmt.Errorf("%w: HTTP %d", ErrProviderUnavailable, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil {
		return ErrProviderUnavailable
	}
	if len(data) > 1<<20 || json.Unmarshal(data, out) != nil {
		return ErrProviderContract
	}
	return nil
}

func (p *openBao) revoke(ctx context.Context, id string) error {
	if id == "" {
		return ErrProviderContract
	}
	return p.call(ctx, http.MethodPut, "sys/leases/revoke", map[string]any{"lease_id": id, "sync": true}, nil)
}

func (p *openBao) expiry(ctx context.Context, id string) (time.Time, error) {
	var response struct {
		Data struct {
			ID         string    `json:"id"`
			ExpireTime time.Time `json:"expire_time"`
		} `json:"data"`
	}
	if err := p.call(ctx, http.MethodPut, "sys/leases/lookup", map[string]string{"lease_id": id}, &response); err != nil {
		return time.Time{}, err
	}
	if response.Data.ID != id || response.Data.ExpireTime.IsZero() {
		return time.Time{}, ErrProviderContract
	}
	return response.Data.ExpireTime, nil
}

func mappedCredential(binding OpenBaoBinding, data json.RawMessage) (string, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) == 0 {
		return "", ErrProviderContract
	}
	var value string
	if binding.JSON {
		encoded, err := json.Marshal(fields)
		if err != nil {
			return "", ErrProviderContract
		}
		value = string(encoded)
	} else if json.Unmarshal(fields[binding.Field], &value) != nil {
		return "", ErrProviderContract
	}
	if value == "" || len(value) > maxValueBytes {
		return "", ErrProviderContract
	}
	return value, nil
}
