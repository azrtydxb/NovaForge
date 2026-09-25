package ci_test

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

// HTTP protocol fixture, not evidence of underlying credential validity;
// internal/secrets/TestOpenBaoPostgresCredential proves real enforcement.
type credentialProvider struct {
	revokeFails bool
	entered     chan struct{}
	release     chan struct{}
	mu          sync.Mutex
	values      map[string]string
	leases      map[string]time.Time
}

func (s *credentialStack) setupProvider(t *testing.T) {
	p := &credentialProvider{values: map[string]string{}, leases: map[string]time.Time{}}
	s.provider = p
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Vault-Token") != "fixture-token" {
			w.WriteHeader(403)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/database/creds/") {
			if p.entered != nil {
				close(p.entered)
				<-p.release
				p.entered = nil
			}
			key := strings.TrimPrefix(r.URL.Path, "/v1/database/creds/")
			value, ok := p.values[key]
			if !ok {
				w.WriteHeader(404)
				return
			}
			id := "database/creds/" + key + "/" + uuid.NewString()
			p.leases[id] = time.Now().Add(time.Minute).Truncate(time.Microsecond)
			var data any = map[string]string{"value": value}
			if key == "staging/JSON_LOGIN" {
				// Preserve valid JSON exactly as a provider may send it. A
				// string round trip would normalize away surrogate escapes.
				if !json.Valid([]byte(value)) {
					w.WriteHeader(500)
					return
				}
				data = json.RawMessage(value)
			}
			json.NewEncoder(w).Encode(map[string]any{"lease_id": id, "lease_duration": 60, "data": data})
			return
		}
		var body struct {
			ID string `json:"lease_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/v1/sys/leases/lookup":
			exp, ok := p.leases[body.ID]
			if !ok {
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": body.ID, "expire_time": exp}})
		case "/v1/sys/leases/revoke":
			if p.revokeFails {
				w.WriteHeader(503)
				return
			}
			delete(p.leases, body.ID)
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("fixture-token"), 0600); err != nil {
		t.Fatal(err)
	}
	var bindings []secrets.OpenBaoBinding
	for _, pair := range [][2]string{{"DEPLOY_TOKEN", "staging"}, {"PROD_KEY", "production"}, {"MISSING_SECOND", "staging"}} {
		bindings = append(bindings, secrets.OpenBaoBinding{OrgID: s.org, Name: pair[0], Environment: pair[1], Path: "database/creds/" + pair[1] + "/" + pair[0], Method: "GET", Field: "value"})
	}
	bindings = append(bindings, secrets.OpenBaoBinding{OrgID: s.org, Name: "JSON_LOGIN", Environment: "staging", Path: "database/creds/staging/JSON_LOGIN", Method: "GET", JSON: true})
	var err error
	s.broker, err = secrets.NewOpenBaoBroker(s.pool, []byte("credential-stack-kek"), secrets.OpenBaoConfig{Endpoint: server.URL, TokenFile: token, Bindings: bindings})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.pool.Exec(context.Background(), "DELETE FROM secrets.credential_attempts WHERE org_id=$1", s.org)
		s.pool.Exec(context.Background(), "DELETE FROM secrets.credential_scopes WHERE org_id=$1", s.org)
	})
}
func (s *credentialStack) putDynamicSecret(name, env, value string) error {
	s.provider.mu.Lock()
	defer s.provider.mu.Unlock()
	s.provider.values[env+"/"+name] = value
	return nil
}
