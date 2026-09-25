package reviews

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
)

// Every test owns a random real database. The supplied URL is used only to
// CREATE/DROP that database; shared schemas are never migrated or truncated.
func executionTestStore(t *testing.T) *Store {
	t.Helper()
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	name := "nf_review_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Logf("owned database created: %s", name)
	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP DATABASE "+quoted); err != nil {
			t.Errorf("owned database drop %s: %v", name, err)
		} else {
			t.Logf("owned database dropped: %s", name)
		}
		admin.Close()
	})
	if err = database.Migrate(u.String(), "reviews", MigrationsFS); err != nil {
		t.Fatal(err)
	}
	pool, err = database.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(pool)
}

func executionScope(org uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorKind: "service", ServiceName: "work-reviews"})
}
func executionRequest(t *testing.T, s *Store, org uuid.UUID) (context.Context, reviewRequest, uuid.UUID) {
	t.Helper()
	ctx := executionScope(org)
	run, err := s.CreateRun(ctx, Run{OrgID: org, RepoID: uuid.New(), Title: "SDK contract fixture", SourceRef: "feature", TargetRef: "main", AuthorID: uuid.New(), AuthorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	r := reviewRequest{ID: uuid.New(), OrgID: org, RunID: run.ID, RequestedBy: uuid.New(), SourceSHA: strings.Repeat("a", 40), TargetSHA: strings.Repeat("a", 40), State: "running", LeaseID: uuid.New(), LeaseUntil: time.Now().Add(time.Minute)}
	_, err = s.pool.Exec(ctx, `INSERT INTO reviews.agent_review_requests(id,org_id,run_id,requested_by,source_sha,target_sha,state,lease_id,lease_until) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, r.ID, org, run.ID, r.RequestedBy, r.SourceSHA, r.TargetSHA, r.State, r.LeaseID, r.LeaseUntil)
	if err != nil {
		t.Fatal(err)
	}
	attempt := uuid.New()
	if err = s.beginReviewExecution(ctx, r, attempt); err != nil {
		t.Fatal(err)
	}
	return ctx, r, attempt
}

const executionCapabilities = `{"version":1,"execution_id_header":"X-Execution-Id","request_digest":"sha256-exact-body","replay":"never","protocols":["openai"],"stream":false,"structured_output":"one-forced-function-tool","status_path":"/executions/v1/{execution_id}","request_bytes":1048576,"response_bytes":8388608}`
const executionCompletion = `{"id":"completion-fixture","object":"chat.completion","model":"review-fixture","created":1767225600,"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"output-fixture","type":"function","function":{"name":"output","arguments":"{\"verdict\":\"approve\",\"summary\":\"controlled fixture\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

type executionOwnerFixture struct {
	t                             *testing.T
	store                         *Store
	server                        *httptest.Server
	mu                            sync.Mutex
	receipts                      map[string]executionReceipt
	bodies                        map[string][]byte
	posts, statusCalls            int
	postMode, statusMode, capMode string
	entered                       chan struct{}
	release                       chan struct{}
}

func executionOwner(t *testing.T, s *Store) *executionOwnerFixture {
	t.Helper()
	f := &executionOwnerFixture{t: t, store: s, receipts: map[string]executionReceipt{}, bodies: map[string][]byte{}}
	f.server = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	f.server.EnableHTTP2 = true
	f.server.StartTLS()
	t.Cleanup(f.server.Close)
	return f
}
func (f *executionOwnerFixture) gateway(t *testing.T, s *Store, key string) *ExecutionGateway {
	t.Helper()
	g, err := newExecutionGateway(context.Background(), s, f.server.URL+"/executions/v1", key, "review-fixture", f.server.Client().Transport.(*http.Transport))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.client.CloseIdleConnections)
	return g
}
func (f *executionOwnerFixture) modes(post, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.postMode, f.statusMode = post, status
}
func (f *executionOwnerFixture) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts, f.statusCalls
}
func (f *executionOwnerFixture) complete(id uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.receipts[id.String()]
	now := time.Now().UTC().Truncate(time.Microsecond)
	r.Phase, r.Terminal, r.UpstreamStatus, r.CompletedAt = "completed", true, 200, &now
	f.receipts[id.String()] = r
}
func (f *executionOwnerFixture) serve(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if req.Header.Get("Authorization") != "Bearer original-principal" && req.Header.Get("Authorization") != "Bearer rotated-same-principal" {
		// A different active principal can negotiate capabilities but not read
		// another principal's receipt. Revoked credentials fail authentication.
		if req.Header.Get("Authorization") == "Bearer foreign-principal" && strings.HasSuffix(req.URL.Path, "/capabilities") {
			_, _ = io.WriteString(w, executionCapabilities)
			return
		}
		if req.Header.Get("Authorization") == "Bearer foreign-principal" {
			http.Error(w, "missing", 404)
		} else {
			http.Error(w, "auth", 401)
		}
		return
	}
	f.mu.Lock()
	postMode, statusMode, capMode := f.postMode, f.statusMode, f.capMode
	f.mu.Unlock()
	if req.URL.Path == "/executions/v1/capabilities" {
		switch capMode {
		case "html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>not an owner</html>")
		case "version":
			_, _ = io.WriteString(w, strings.Replace(executionCapabilities, `"version":1`, `"version":2`, 1))
		case "redirect":
			http.Redirect(w, req, f.server.URL+"/redirect-target", 307)
		case "missing":
			http.NotFound(w, req)
		default:
			_, _ = io.WriteString(w, executionCapabilities)
		}
		return
	}
	if req.URL.Path == "/redirect-target" {
		f.t.Error("redirect was followed")
		http.Error(w, "forbidden redirect", 500)
		return
	}
	if req.Method == http.MethodPost && req.URL.Path == "/executions/v1/chat/completions" {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			f.t.Error(err)
			http.Error(w, "body", 400)
			return
		}
		id := req.Header.Get("X-Execution-Id")
		sum := sha256.Sum256(body)
		digest := hex.EncodeToString(sum[:])
		var owner, stored string
		// A separate pooled connection observes acknowledged durable binding
		// before the server admits even this synthetic model invocation.
		err = f.store.pool.QueryRow(req.Context(), `SELECT owner_url,request_digest FROM reviews.review_executions WHERE attempt_id=$1`, id).Scan(&owner, &stored)
		if err != nil || stored != digest || owner != f.server.URL+"/executions/v1" {
			f.t.Errorf("POST before exact binding: owner=%q digest=%q err=%v", owner, stored, err)
			http.Error(w, "unbound", 500)
			return
		}
		if req.ProtoMajor != 1 {
			f.t.Error("managed POST negotiated retry-capable HTTP/2")
		}
		if len(req.Header.Values("X-Execution-Id")) != 1 || len(req.Header.Values("Authorization")) != 1 || req.Header.Get("Content-Type") != "application/json" {
			f.t.Error("invalid managed request headers")
		}
		f.mu.Lock()
		f.posts++
		if _, duplicate := f.receipts[id]; duplicate {
			f.mu.Unlock()
			http.Error(w, "execution_replay", 409)
			return
		}
		f.bodies[id] = body
		f.receipts[id] = executionReceipt{Version: 1, ExecutionID: id, Digest: digest, Phase: "uncertain", CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
		f.mu.Unlock()
		if f.entered != nil {
			select {
			case f.entered <- struct{}{}:
			default:
			}
		}
		if f.release != nil {
			<-f.release
		}
		if postMode == "lost" {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				f.t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		if postMode == "unknown" {
			http.Error(w, "execution_unknown", 503)
			return
		}
		if postMode == "redirect" {
			http.Redirect(w, req, f.server.URL+"/redirect-target", 307)
			return
		}
		f.complete(uuid.MustParse(id))
		w.Header().Set("X-Execution-Id", id)
		w.Header().Set("X-Execution-Digest", digest)
		w.Header().Set("X-Execution-Terminal", "true")
		switch postMode {
		case "wrong-id":
			w.Header().Set("X-Execution-Id", uuid.NewString())
		case "wrong-digest":
			w.Header().Set("X-Execution-Digest", strings.Repeat("f", 64))
		case "nonterminal":
			w.Header().Set("X-Execution-Terminal", "false")
		case "missing":
			w.Header().Del("X-Execution-Terminal")
		case "duplicate":
			w.Header().Add("X-Execution-Terminal", "true")
		case "oversized":
			_, _ = io.WriteString(w, strings.Repeat(" ", executionResponseBytes+1))
			return
		case "truncated":
			w.Header().Set("Content-Length", fmt.Sprint(len(executionCompletion)+20))
		}
		_, _ = io.WriteString(w, executionCompletion)
		return
	}
	if req.Method != http.MethodGet {
		http.NotFound(w, req)
		return
	}
	id := strings.TrimPrefix(req.URL.Path, "/executions/v1/")
	f.mu.Lock()
	f.statusCalls++
	r, ok := f.receipts[id]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, req)
		return
	}
	switch statusMode {
	case "401":
		http.Error(w, "auth", 401)
		return
	case "403":
		http.Error(w, "auth", 403)
		return
	case "404":
		http.NotFound(w, req)
		return
	case "503":
		http.Error(w, "unavailable", 503)
		return
	case "timeout":
		<-req.Context().Done()
		return
	case "redirect":
		http.Redirect(w, req, f.server.URL+"/redirect-target", 307)
		return
	case "wrong-id":
		r.ExecutionID = uuid.NewString()
	case "wrong-digest":
		r.Digest = strings.Repeat("0", 64)
	case "version":
		r.Version = 2
	case "nonterminal":
		r.Terminal = false
	case "phase":
		r.Phase = "uncertain"
	case "status":
		r.UpstreamStatus = 429
	case "missing-completion":
		r.CompletedAt = nil
	case "zero-completion":
		r.CompletedAt = new(time.Time)
	case "missing-creation":
		r.CreatedAt = time.Time{}
	case "malformed":
		_, _ = io.WriteString(w, `{"version":`)
		return
	case "html":
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>status</html>")
		return
	case "oversized":
		_, _ = io.WriteString(w, strings.Repeat(" ", executionStatusBytes+1))
		return
	}
	raw, _ := json.Marshal(r)
	if statusMode == "duplicate" {
		raw = append([]byte(`{"terminal":false,`), raw[1:]...)
	}
	_, _ = w.Write(raw)
}

func invokeExecution(g *ExecutionGateway, ctx context.Context, r reviewRequest, id uuid.UUID) (reviewOutput, provider.Usage, bool, error) {
	ctx = context.WithValue(ctx, reviewExecutionKey{}, reviewExecutionContext{request: r, attempt: id})
	reviewer := &AgentReviewer{Models: []provider.LanguageModel{g.model}, ProviderOptions: map[string]any{"gateway": map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}}}}
	return reviewer.reviewBounded(ctx, Run{Title: "SDK contract fixture", SourceRef: "feature", TargetRef: "main", AgentName: "author"}, "security", "source-sha", "target-sha", "+ controlled source fixture", 4096)
}
func executionHeld(t *testing.T, s *Store, id uuid.UUID, want bool) {
	t.Helper()
	var held bool
	if err := s.pool.QueryRow(context.Background(), `SELECT terminated_at IS NULL FROM reviews.review_executions WHERE attempt_id=$1`, id).Scan(&held); err != nil || held != want {
		t.Fatalf("held=%v want=%v err=%v", held, want, err)
	}
}
func executionBinding(t *testing.T, s *Store, id uuid.UUID) outstandingExecution {
	t.Helper()
	e := outstandingExecution{AttemptID: id}
	if err := s.pool.QueryRow(context.Background(), `SELECT owner_url,request_digest FROM reviews.review_executions WHERE attempt_id=$1`, id).Scan(&e.Owner, &e.Digest); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestExecutionGatewaySDKBindingAndNoReplay(t *testing.T) {
	s := executionTestStore(t)
	f := executionOwner(t, s)
	g := f.gateway(t, s, "original-principal")
	ctx, r, id := executionRequest(t, s, uuid.New())
	out, usage, completed, err := invokeExecution(g, ctx, r, id)
	if err != nil || !completed || out.Verdict != "approve" || usage.TotalTokens != 15 {
		t.Fatalf("actual SDK response: %+v %+v %v %v", out, usage, completed, err)
	}
	executionHeld(t, s, id, false)
	f.mu.Lock()
	body := f.bodies[id.String()]
	f.mu.Unlock()
	var wire map[string]json.RawMessage
	if err = json.Unmarshal(body, &wire); err != nil || len(wire) != 6 {
		t.Fatalf("SDK wire fields: %s %v", body, err)
	}
	for _, field := range []string{"model", "messages", "max_completion_tokens", "tools", "tool_choice", "chat_template_kwargs"} {
		if wire[field] == nil {
			t.Fatalf("missing %s", field)
		}
	}
	var choice struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err = json.Unmarshal(wire["tool_choice"], &choice); err != nil || choice.Type != "function" || choice.Function.Name != "output" {
		t.Fatalf("forced synthetic tool missing: %s", body)
	}
	if _, _, completed, err = invokeExecution(g, ctx, r, id); err == nil || completed {
		t.Fatal("duplicate invocation was replayed")
	}
	if posts, _ := f.counts(); posts != 1 {
		t.Fatalf("SDK/transport replayed POST: %d", posts)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE reviews.review_executions SET owner_url='https://wrong.test/executions/v1' WHERE attempt_id=$1`, id); err == nil {
		t.Fatal("immutable owner changed")
	}
	if _, err = s.pool.Exec(ctx, `DELETE FROM reviews.review_executions WHERE attempt_id=$1`, id); err == nil {
		t.Fatal("execution obligation deleted")
	}
}

func TestExecutionGatewayStatusProofFailsClosed(t *testing.T) {
	s := executionTestStore(t)
	f := executionOwner(t, s)
	g := f.gateway(t, s, "original-principal")
	ctx, r, id := executionRequest(t, s, uuid.New())
	f.modes("lost", "")
	if _, _, completed, err := invokeExecution(g, ctx, r, id); err == nil || completed {
		t.Fatal("lost response became SDK success")
	}
	e := executionBinding(t, s, id)
	f.complete(id)
	for _, mode := range []string{"401", "403", "404", "503", "timeout", "redirect", "wrong-id", "wrong-digest", "version", "nonterminal", "phase", "status", "missing-completion", "zero-completion", "missing-creation", "malformed", "html", "oversized", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			f.modes("lost", mode)
			if err := g.reconcileOne(ctx, e); err == nil {
				t.Fatal("invalid receipt retired obligation")
			}
			executionHeld(t, s, id, true)
		})
	}
	f.modes("lost", "")
	foreign := f.gateway(t, s, "foreign-principal")
	if err := foreign.reconcileOne(ctx, e); err == nil {
		t.Fatal("foreign principal reconciled")
	}
	executionHeld(t, s, id, true)
	wrongOwner := e
	wrongOwner.Owner = "https://different-owner.test/executions/v1"
	_, before := f.counts()
	if err := g.reconcileOne(ctx, wrongOwner); err == nil {
		t.Fatal("wrong owner reconciled")
	}
	if _, after := f.counts(); before != after {
		t.Fatal("wrong-owner status made network call")
	}
	if err := g.reconcileOne(executionScope(uuid.New()), e); err == nil {
		t.Fatal("cross-org receipt accepted")
	}
	executionHeld(t, s, id, true)
	rotated := f.gateway(t, s, "rotated-same-principal")
	if err := rotated.reconcileOne(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := rotated.reconcileOne(ctx, e); err != nil {
		t.Fatalf("duplicate receipt not idempotent: %v", err)
	}
	executionHeld(t, s, id, false)
	if posts, _ := f.counts(); posts != 1 {
		t.Fatalf("reconciliation repeated model: %d", posts)
	}
}

func TestExecutionGatewayResponseProofFailsClosed(t *testing.T) {
	s := executionTestStore(t)
	f := executionOwner(t, s)
	g := f.gateway(t, s, "original-principal")
	for _, mode := range []string{"wrong-id", "wrong-digest", "nonterminal", "missing", "duplicate", "oversized", "truncated", "redirect", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			f.modes(mode, "")
			ctx, r, id := executionRequest(t, s, uuid.New())
			before, _ := f.counts()
			if _, usage, completed, err := invokeExecution(g, ctx, r, id); err == nil || completed || usage.TotalTokens != 0 {
				t.Fatalf("invalid proof became completion: %+v %v %v", usage, completed, err)
			}
			executionHeld(t, s, id, true)
			if after, _ := f.counts(); after != before+1 {
				t.Fatal("SDK retried uncertain completion")
			}
		})
	}
}

func TestExecutionGatewayDBErrorBeforeSendAndImmutableBinding(t *testing.T) {
	s := executionTestStore(t)
	f := executionOwner(t, s)
	g := f.gateway(t, s, "original-principal")
	ctx, r, id := executionRequest(t, s, uuid.New())
	_, err := s.pool.Exec(ctx, `CREATE FUNCTION reviews.reject_test_binding() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'controlled binding storage failure'; END $$; CREATE TRIGGER reject_test_binding BEFORE UPDATE ON reviews.review_executions FOR EACH ROW EXECUTE FUNCTION reviews.reject_test_binding()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, completed, err := invokeExecution(g, ctx, r, id); err == nil || completed {
		t.Fatal("database failure sent model")
	}
	if posts, _ := f.counts(); posts != 0 {
		t.Fatal("network before durable binding")
	}
	executionHeld(t, s, id, true)
	if _, err = s.pool.Exec(ctx, `DROP TRIGGER reject_test_binding ON reviews.review_executions; DROP FUNCTION reviews.reject_test_binding()`); err != nil {
		t.Fatal(err)
	}
	f.modes("lost", "")
	if _, _, _, err = invokeExecution(g, ctx, r, id); err == nil {
		t.Fatal("expected lost response")
	}
	for _, column := range []string{"owner_url", "request_digest"} {
		value := "https://different.test/executions/v1"
		if column == "request_digest" {
			value = strings.Repeat("0", 64)
		}
		if _, err = s.pool.Exec(ctx, `UPDATE reviews.review_executions SET `+column+`=$2 WHERE attempt_id=$1`, id, value); err == nil {
			t.Fatalf("binding %s mutated", column)
		}
	}
	if _, _, _, err = invokeExecution(g, ctx, r, id); err == nil {
		t.Fatal("bound unknown replayed")
	}
	if posts, _ := f.counts(); posts != 1 {
		t.Fatalf("duplicate sent %d POSTs", posts)
	}
	ctx2, r2, id2 := executionRequest(t, s, uuid.New())
	if _, err = s.pool.Exec(ctx2, `UPDATE reviews.agent_review_requests SET lease_until=now()-interval '1 second' WHERE id=$1`, r2.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = invokeExecution(g, ctx2, r2, id2); err == nil {
		t.Fatal("expired pre-dispatch lease invoked model")
	}
	if posts, _ := f.counts(); posts != 1 {
		t.Fatal("expired lease sent model")
	}
	ctx3, r3, id3 := executionRequest(t, s, uuid.New())
	ctx3 = context.WithValue(ctx3, reviewExecutionKey{}, reviewExecutionContext{request: r3, attempt: id3})
	req, _ := http.NewRequestWithContext(ctx3, http.MethodPost, g.owner+"/chat/completions", strings.NewReader(strings.Repeat("x", executionRequestBytes+1)))
	if _, err = g.RoundTrip(req); err == nil {
		t.Fatal("oversized SDK request sent")
	}
	if posts, _ := f.counts(); posts != 1 {
		t.Fatal("oversized request sent model")
	}
}

func TestExecutionGatewayPreflightFailClosed(t *testing.T) {
	s := executionTestStore(t)
	f := executionOwner(t, s)
	transport := f.server.Client().Transport.(*http.Transport)
	for _, owner := range []string{"", strings.Replace(f.server.URL, "https:", "http:", 1) + "/executions/v1", f.server.URL + "/v1", f.server.URL + "/executions/v1?x=y", f.server.URL + "/executions/v1/"} {
		if _, err := newExecutionGateway(context.Background(), s, owner, "original-principal", "review-fixture", transport); err == nil {
			t.Fatalf("unsafe owner accepted %q", owner)
		}
	}
	for _, mode := range []string{"html", "version", "redirect", "missing"} {
		f.mu.Lock()
		f.capMode = mode
		f.mu.Unlock()
		if _, err := newExecutionGateway(context.Background(), s, f.server.URL+"/executions/v1", "original-principal", "review-fixture", transport); err == nil {
			t.Fatalf("invalid preflight %s accepted", mode)
		}
	}
	f.mu.Lock()
	f.capMode = ""
	f.mu.Unlock()
	for _, key := range []string{"", "revoked", "bad\nkey"} {
		if _, err := newExecutionGateway(context.Background(), s, f.server.URL+"/executions/v1", key, "review-fixture", transport); err == nil {
			t.Fatal("missing/invalid auth accepted")
		}
	}
	worker := &ReviewWorker{Store: s, Config: ReviewConfig{WallclockSeconds: 60, MaxInputBytes: 4096, MaxOutputTokens: 128, MaxConcurrentRequests: 1, MaxRoles: 1}}
	if err := worker.Tick(context.Background()); err == nil {
		t.Fatal("worker accepted missing managed owner")
	}
	if posts, _ := f.counts(); posts != 0 {
		t.Fatal("preflight invoked model")
	}
}
