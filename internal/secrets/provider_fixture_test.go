package secrets_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/secrets"
)

// Protocol fixture only; TestOpenBaoPostgresCredential is the separate test
// that proves issued material works, expires and is revoked at a real target.
type baoFixture struct {
	entered     chan struct{}
	release     chan struct{}
	mu          sync.Mutex
	expires     time.Time
	duration    int64
	static      bool
	revokeFails bool
	leases      map[string]bool
	calls       int
	server      *httptest.Server
	config      secrets.OpenBaoConfig
}

func dynamicBroker(t *testing.T, ttl time.Duration) (*secrets.Broker, context.Context, uuid.UUID, *baoFixture) {
	t.Helper()
	pool := brokerPool(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	f := &baoFixture{expires: time.Now().Add(ttl).Truncate(time.Microsecond), duration: 1, leases: map[string]bool{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		if r.Header.Get("X-Vault-Token") != "protocol-test-token" {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v1/database/creds/") {
			if f.entered != nil {
				close(f.entered)
				<-f.release
			}
			id := "database/creds/fixture/" + uuid.NewString()
			if f.static {
				id = ""
			} else {
				f.leases[id] = true
			}
			json.NewEncoder(w).Encode(map[string]any{"lease_id": id, "lease_duration": f.duration, "data": map[string]string{"value": "issued-value"}})
			return
		}
		var body struct {
			ID string `json:"lease_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/v1/sys/leases/lookup":
			if !f.leases[body.ID] {
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": body.ID, "expire_time": f.expires}})
		case "/v1/sys/leases/revoke":
			if f.revokeFails {
				w.WriteHeader(503)
				return
			}
			delete(f.leases, body.ID)
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.server.Close)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("protocol-test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := secrets.OpenBaoConfig{Endpoint: f.server.URL, TokenFile: token}
	for _, pair := range [][2]string{{"TOKEN", "staging"}, {"TOKEN", "production"}, {"API_KEY", "staging"}, {"SECRET", "staging"}, {"AWS_STAGING", "staging"}, {"DEPLOY_KEY", "production"}} {
		cfg.Bindings = append(cfg.Bindings, secrets.OpenBaoBinding{OrgID: org, Name: pair[0], Environment: pair[1], Path: "database/creds/" + pair[1], Method: "GET", Field: "value"})
	}
	f.config = cfg
	b, err := secrets.NewOpenBaoBroker(pool, []byte("protocol-test-kek"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM secrets.credential_attempts WHERE org_id=$1", org)
		pool.Exec(context.Background(), "DELETE FROM secrets.credential_scopes WHERE org_id=$1", org)
		if _, err := pool.Exec(context.Background(), "DELETE FROM secrets.secret_leases WHERE org_id=$1", org); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(context.Background(), "DELETE FROM secrets.secret_values WHERE org_id=$1", org); err != nil {
			t.Error(err)
		}
	})
	return b, ctx, org, f
}
