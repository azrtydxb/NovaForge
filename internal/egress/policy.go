// Package egress enforces operator-owned destinations before external I/O.
package egress

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Rule struct {
	Scheme string   `json:"scheme"`
	Host   string   `json:"host"`
	Port   int      `json:"port"`
	CIDRs  []string `json:"cidrs"`
}
type Policy struct{ Rules []Rule }

func Parse(raw string) (*Policy, error) {
	p := &Policy{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &p.Rules); err != nil {
			return nil, fmt.Errorf("outbound destination configuration: %w", err)
		}
	}
	for _, r := range p.Rules {
		if (r.Scheme != "http" && r.Scheme != "https") || r.Host == "" || strings.ContainsAny(r.Host, "/@* ") || r.Port < 1 || r.Port > 65535 || len(r.CIDRs) == 0 {
			return nil, fmt.Errorf("invalid outbound destination rule")
		}
		for _, raw := range r.CIDRs {
			if _, err := netip.ParsePrefix(raw); err != nil {
				return nil, fmt.Errorf("invalid outbound destination CIDR: %w", err)
			}
		}
	}
	return p, nil
}

// Resolve returns a checked destination and a pinned IP. Private addresses are
// allowed only when explicitly contained in a private CIDR, never by 0/0.
// Loopback, link-local (including cloud metadata), unspecified and multicast
// addresses are always refused. No ambient proxy is used.
func (p *Policy) Resolve(ctx context.Context, raw string) (*url.URL, string, error) {
	refuse := func() (*url.URL, string, error) {
		return nil, "", fmt.Errorf("outbound destination refused by operator policy")
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" {
		return refuse()
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else if u.Scheme == "http" {
			port = "80"
		} else {
			return refuse()
		}
	}
	var rule *Rule
	for i := range p.Rules {
		r := &p.Rules[i]
		if strings.EqualFold(r.Host, u.Hostname()) && r.Scheme == u.Scheme && strconv.Itoa(r.Port) == port {
			rule = r
			break
		}
	}
	if rule == nil {
		return refuse()
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
	if err != nil {
		return nil, "", fmt.Errorf("resolve approved destination: %w", err)
	}
	for _, ip := range addresses {
		ip = ip.Unmap()
		if !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		for _, rawPrefix := range rule.CIDRs {
			prefix, _ := netip.ParsePrefix(rawPrefix)
			if prefix.Contains(ip) && (!ip.IsPrivate() || prefix.Addr().IsPrivate()) {
				return u, ip.String(), nil
			}
		}
	}
	return refuse()
}

func (p *Policy) GitConfig(ctx context.Context, remote string) ([]string, error) {
	u, ip, err := p.Resolve(ctx, remote)
	if err != nil {
		return nil, err
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if strings.Contains(ip, ":") {
		ip = "[" + ip + "]"
	}
	return []string{"-c", "http.proxy=", "-c", "http.followRedirects=false", "-c", "http.curloptResolve=", "-c", "http.curloptResolve=" + u.Hostname() + ":" + port + ":" + ip}, nil
}

func (p *Policy) Client() *http.Client { return p.ClientWithTLS(nil) }

// ClientWithTLS permits operator CA roots while retaining destination enforcement.
func (p *Policy) ClientWithTLS(config *tls.Config) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if config != nil {
		transport.TLSClientConfig = config.Clone()
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		// RoundTrip resolves and pins once, retaining the original URL for TLS/SNI.
		pinned, ok := ctx.Value(addressKey{}).(string)
		if !ok {
			return nil, fmt.Errorf("missing approved destination")
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, pinned)
	}
	// Disable pooling across requests so each connection uses its freshly checked
	// address, including after a DNS or operator policy change.
	transport.DisableKeepAlives = true
	return &http.Client{Timeout: 10 * time.Second, Transport: roundTripper{p, transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("outbound redirects are refused") }}
}

type addressKey struct{}
type roundTripper struct {
	p *Policy
	t *http.Transport
}

func (r roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	u, ip, err := r.p.Resolve(req.Context(), req.URL.String())
	if err != nil {
		return nil, err
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return r.t.RoundTrip(req.Clone(context.WithValue(req.Context(), addressKey{}, net.JoinHostPort(ip, port))))
}
