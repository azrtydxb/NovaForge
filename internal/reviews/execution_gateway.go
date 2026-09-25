package reviews

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/go-ai-sdk/providers/gateway"
	"github.com/google/uuid"
)

const (
	executionRequestBytes  = 1 << 20
	executionResponseBytes = 8 << 20
	executionStatusBytes   = 64 << 10
)

// ExecutionGateway is the authenticated, immutable managed owner for review
// calls. Construct it at startup; capability negotiation failure must prevent
// activation. This is wire preflight, not qualification of a configured model.
// Keep the original owner and principal available until its obligations retire:
// changing configuration never authorizes another owner to vouch for old work.
type ExecutionGateway struct {
	store                       *Store
	owner, key                  string
	client                      *http.Client
	model                       provider.LanguageModel
	requestBytes, responseBytes int64
}

// NewExecutionGateway accepts only an explicit HTTPS /executions/v1 endpoint.
// The ordinary inference endpoint is deliberately not a fallback. Certificates
// use the system trust roots; credentials are held in memory, never persisted.
func NewExecutionGateway(ctx context.Context, store *Store, owner, key, model string) (*ExecutionGateway, error) {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("managed execution requires a standard TLS transport")
	}
	return newExecutionGateway(ctx, store, owner, key, model, transport)
}

func newExecutionGateway(ctx context.Context, store *Store, owner, key, model string, transport *http.Transport) (*ExecutionGateway, error) {
	u, err := url.Parse(owner)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "/executions/v1" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.String() != owner {
		return nil, fmt.Errorf("explicit HTTPS managed execution owner required")
	}
	if store == nil || store.pool == nil || key == "" || strings.IndexFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 || model == "" {
		return nil, fmt.Errorf("managed execution store, credential and model required")
	}
	if transport == nil || (transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify) {
		return nil, fmt.Errorf("authenticated TLS transport required")
	}
	transport = transport.Clone()
	transport.DisableKeepAlives = true // no pooled-connection replay
	transport.ResponseHeaderTimeout = 305 * time.Second
	// HTTP/2 can transparently retry a refused stream. Use one fresh HTTP/1
	// connection per managed call; the SDK's retries are disabled separately.
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	g := &ExecutionGateway{store: store, owner: owner, key: key, requestBytes: executionRequestBytes, responseBytes: executionResponseBytes,
		client: &http.Client{Transport: transport, Timeout: 310 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if err = g.preflight(ctx); err != nil {
		return nil, err
	}
	sdkClient := &http.Client{Transport: g, Timeout: 310 * time.Second, CheckRedirect: g.client.CheckRedirect}
	g.model = gateway.New(gateway.WithBaseURL(owner), gateway.WithAPIKey(key), gateway.WithHTTPClient(sdkClient)).Model(model)
	return g, nil
}

func (g *ExecutionGateway) preflight(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	body, err := g.get(ctx, g.owner+"/capabilities")
	if err != nil {
		return fmt.Errorf("managed execution preflight: %w", err)
	}
	var c struct {
		Version       int      `json:"version"`
		Header        string   `json:"execution_id_header"`
		Digest        string   `json:"request_digest"`
		Replay        string   `json:"replay"`
		Protocols     []string `json:"protocols"`
		Stream        *bool    `json:"stream"`
		Structured    string   `json:"structured_output"`
		StatusPath    string   `json:"status_path"`
		RequestBytes  int64    `json:"request_bytes"`
		ResponseBytes int64    `json:"response_bytes"`
	}
	if err = decodeExecutionJSON(body, &c); err != nil {
		return fmt.Errorf("invalid execution capabilities")
	}
	if c.Version != 1 || c.Header != "X-Execution-Id" || c.Digest != "sha256-exact-body" || c.Replay != "never" || len(c.Protocols) != 1 || c.Protocols[0] != "openai" || c.Stream == nil || *c.Stream || c.Structured != "one-forced-function-tool" || c.StatusPath != "/executions/v1/{execution_id}" || c.RequestBytes < 1 || c.RequestBytes > executionRequestBytes || c.ResponseBytes < 1 || c.ResponseBytes > executionResponseBytes {
		return fmt.Errorf("unsupported managed execution capabilities")
	}
	g.requestBytes, g.responseBytes = c.RequestBytes, c.ResponseBytes
	return nil
}

type reviewExecutionContext struct {
	request reviewRequest
	attempt uuid.UUID
}
type reviewExecutionKey struct{}

// RoundTrip sees the SDK's FINAL serialization, not an approximation of its
// prompt or tool schema. Commit acknowledgement precedes any request to owner.
func (g *ExecutionGateway) RoundTrip(req *http.Request) (*http.Response, error) {
	x, ok := req.Context().Value(reviewExecutionKey{}).(reviewExecutionContext)
	if !ok || x.attempt == uuid.Nil || req.Method != http.MethodPost || req.URL.String() != g.owner+"/chat/completions" || req.Body == nil {
		return nil, fmt.Errorf("untracked or misrouted review invocation refused")
	}
	body, err := readExecutionBody(req.Body, g.requestBytes)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	e := outstandingExecution{AttemptID: x.attempt, Owner: g.owner, Digest: hex.EncodeToString(digest[:])}
	bindctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
	err = g.store.bindReviewExecution(bindctx, x.request, x.attempt, e.Owner, e.Digest)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("persist execution binding: %w", err)
	}
	wire := req.Clone(req.Context())
	wire.Body = io.NopCloser(bytes.NewReader(body))
	wire.ContentLength = int64(len(body))
	wire.GetBody = nil
	wire.Close = true
	wire.Header = make(http.Header)
	wire.Header.Set("Authorization", "Bearer "+g.key)
	wire.Header.Set("Content-Type", "application/json")
	wire.Header.Set("X-Execution-Id", x.attempt.String())
	resp, err := g.client.Do(wire)
	if err != nil {
		return nil, fmt.Errorf("managed execution outcome unknown")
	}
	defer resp.Body.Close()
	raw, err := readExecutionBody(resp.Body, g.responseBytes)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("managed execution HTTP %d; outcome unconfirmed", resp.StatusCode)
	}
	if !executionJSON(resp.Header) || !singleExecutionHeader(resp.Header, "X-Execution-Id", x.attempt.String()) || !singleExecutionHeader(resp.Header, "X-Execution-Digest", e.Digest) || !singleExecutionHeader(resp.Header, "X-Execution-Terminal", "true") {
		return nil, fmt.Errorf("invalid managed execution completion proof")
	}
	// Headers prove the live response; only the versioned durable status receipt
	// retires admission. Do not synthesize a response or usage from a late receipt.
	if err = g.reconcileOne(req.Context(), e); err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	return resp, nil
}

func singleExecutionHeader(h http.Header, key, want string) bool {
	values := h.Values(key)
	return len(values) == 1 && values[0] == want
}
func executionJSON(h http.Header) bool {
	kind, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && kind == "application/json"
}
func readExecutionBody(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, fmt.Errorf("managed execution body incomplete or exceeds bound")
	}
	return body, nil
}

// Reject ambiguous top-level duplicate fields rather than let encoding/json's
// last-value-wins behavior select the receipt identity or terminal flag.
func decodeExecutionJSON(body []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("JSON object required")
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok || seen[strings.ToLower(name)] {
			return fmt.Errorf("duplicate or invalid JSON field")
		}
		seen[strings.ToLower(name)] = true
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return err
		}
	}
	if _, err = d.Token(); err != nil {
		return err
	}
	if _, err = d.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return json.Unmarshal(body, out)
}

func (g *ExecutionGateway) get(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.key)
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execution owner unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !executionJSON(resp.Header) {
		return nil, fmt.Errorf("execution owner HTTP %d or non-JSON response", resp.StatusCode)
	}
	return readExecutionBody(resp.Body, executionStatusBytes)
}

type executionReceipt struct {
	Version        int        `json:"version"`
	ExecutionID    string     `json:"execution_id"`
	Digest         string     `json:"request_digest"`
	Phase          string     `json:"phase"`
	Terminal       bool       `json:"terminal"`
	UpstreamStatus int        `json:"upstream_status"`
	CreatedAt      time.Time  `json:"created_at"`
	CompletedAt    *time.Time `json:"completed_at"`
}

func (r executionReceipt) validate(e outstandingExecution) error {
	if r.Version != 1 || e.AttemptID == uuid.Nil || r.ExecutionID != e.AttemptID.String() || len(e.Digest) != 64 || r.Digest != e.Digest || r.Phase != "completed" || !r.Terminal || r.UpstreamStatus != 200 || r.CreatedAt.IsZero() || r.CompletedAt == nil || r.CompletedAt.IsZero() || r.CompletedAt.Before(r.CreatedAt) {
		return fmt.Errorf("execution receipt is not exact terminal completion proof")
	}
	return nil
}
func (g *ExecutionGateway) reconcileOne(ctx context.Context, e outstandingExecution) error {
	if e.Owner != g.owner {
		return fmt.Errorf("execution belongs to a different configured owner; held")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	body, err := g.get(ctx, e.Owner+"/"+e.AttemptID.String())
	if err != nil {
		return err
	}
	var receipt executionReceipt
	if err = decodeExecutionJSON(body, &receipt); err != nil {
		return fmt.Errorf("malformed execution receipt")
	}
	return g.store.confirmReviewReceipt(ctx, e, receipt)
}

// reconcileReviewExecutions is scoped, bounded and independent of request
// display state. Failures remain held and do not starve other receipts.
func (g *ExecutionGateway) reconcileReviewExecutions(ctx context.Context) error {
	dbctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	executions, err := g.store.takeReviewExecutions(dbctx)
	cancel()
	if err != nil {
		return err
	}
	for _, e := range executions {
		if err = g.reconcileOne(ctx, e); err != nil {
			// Do not include credentials, prompt/body data or remote error text.
			// Next sweep retries status only, never model invocation.
			continue
		}
	}
	return nil
}
