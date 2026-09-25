package secrets_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/secrets"
)

// TestOpenBaoPostgresCredential uses an explicitly supplied, verified binary
// and an owned disposable database. It never discovers or uses shared issuer
// credentials. Protocol tests alone cannot establish provider enforcement.
func TestOpenBaoPostgresCredential(t *testing.T) {
	binary := os.Getenv("TEST_OPENBAO_BINARY")
	if binary == "" {
		t.Skip("TEST_OPENBAO_BINARY not set: real OpenBao enforcement NOT tested")
	}
	u, err := url.Parse(dbURL(t))
	if err != nil {
		t.Fatal("invalid test database URL")
	}
	if len(u.Path) < 9 || u.Path[:9] != "/nf_cred_" {
		t.Fatal("real provider test requires lane-owned nf_cred_ disposable database")
	}
	pool := brokerPool(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	token := uuid.NewString()
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "server", "-dev", "-dev-listen-address="+addr)
	cmd.Env = append(os.Environ(), "BAO_DEV_ROOT_TOKEN_ID="+token, "BAO_LOG_LEVEL=error")
	// Dev startup prints its root token. Never capture that stream as evidence.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal("could not start isolated OpenBao")
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	endpoint := "http://" + addr
	client := &http.Client{Timeout: 3 * time.Second}
	ready := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		resp, err := client.Get(endpoint + "/v1/sys/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				ready = true
				break
			}
		}
	}
	if !ready {
		t.Fatal("isolated OpenBao not healthy")
	}
	call := func(method, path string, body any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, endpoint+"/v1/"+path, bytes.NewReader(raw))
		req.Header.Set("X-Vault-Token", token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal("OpenBao setup transport failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			t.Fatalf("OpenBao setup %s: HTTP %d (body withheld)", path, resp.StatusCode)
		}
	}
	call("POST", "sys/mounts/database", map[string]any{"type": "database"})
	cfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	rolePrefix := "nfcred_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16] + "_"
	connection := *u
	connection.User = url.UserPassword("{{username}}", "{{password}}")
	call("POST", "database/config/owned", map[string]any{"plugin_name": "postgresql-database-plugin", "allowed_roles": []string{"owned"}, "connection_url": strings.NewReplacer("%7B", "{", "%7D", "}").Replace(connection.String()), "username": cfg.User, "password": cfg.Password, "username_template": rolePrefix + "{{random 12}}"})
	// A uniquely named role per issued lease, with no permissions on unrelated
	// databases or objects. VALID UNTIL enforces expiry independently of Bao's
	// background lease reaper, including while the reaper is unavailable.
	sql := `CREATE ROLE "{{name}}" WITH LOGIN PASSWORD '{{password}}' VALID UNTIL '{{expiration}}';`
	call("POST", "database/roles/owned", map[string]any{"db_name": "owned", "creation_statements": []string{sql}, "default_ttl": "3s", "max_ttl": "3s"})
	org := uuid.New()
	ctx := scopedCtx(org)
	run := uuid.New()
	broker, err := secrets.NewOpenBaoBroker(pool, []byte("integration-kek"), secrets.OpenBaoConfig{Endpoint: endpoint, TokenFile: tokenPath, Bindings: []secrets.OpenBaoBinding{{OrgID: org, Environment: "staging", Name: "PG_LOGIN", Path: "database/creds/owned", Method: "GET", JSON: true}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = broker.RevokeRunLeases(ctx, run, uuid.Nil)
		pool.Exec(context.Background(), "DELETE FROM secrets.secret_leases WHERE org_id=$1", org)
		pool.Exec(context.Background(), "DELETE FROM secrets.credential_attempts WHERE org_id=$1", org)
		pool.Exec(context.Background(), "DELETE FROM secrets.credential_scopes WHERE org_id=$1", org)
	})
	var issuedUsers []string
	t.Cleanup(func() {
		// Include a role from rejected/mutated issuance whose credential was
		// deliberately never released. Only this fixture's random prefix.
		rows, err := pool.Query(context.Background(), "SELECT rolname FROM pg_roles WHERE starts_with(rolname,$1)", rolePrefix)
		if err != nil {
			t.Error("cannot enumerate owned fixture roles for cleanup")
			return
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Error(err)
				continue
			}
			issuedUsers = append(issuedUsers, name)
		}
		if err := rows.Err(); err != nil {
			t.Error(err)
		}
		rows.Close()
		for _, name := range issuedUsers {
			if !strings.HasPrefix(name, rolePrefix) {
				t.Error("refusing cleanup outside fixture prefix")
				continue
			}
			if _, err := pool.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgx.Identifier{name}.Sanitize()); err != nil {
				t.Errorf("cleanup owned provider role: %v", err)
			}
		}
	})
	connect := func(value string) error {
		var data struct{ Username, Password string }
		if err := json.Unmarshal([]byte(value), &data); err != nil {
			return fmt.Errorf("invalid issued JSON")
		}
		target := *u
		target.User = url.UserPassword(data.Username, data.Password)
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		conn, err := pgx.Connect(cctx, target.String())
		if err == nil {
			err = conn.Ping(cctx)
			conn.Close(cctx)
		}
		return err
	}
	control := func() {
		t.Helper()
		controlCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		conn, err := pgx.Connect(controlCtx, u.String())
		if err != nil {
			t.Fatal("PostgreSQL control connection failed; no credential rejection evidence")
		}
		defer conn.Close(controlCtx)
		if err := conn.Ping(controlCtx); err != nil {
			t.Fatal("PostgreSQL control ping failed; no credential rejection evidence")
		}
	}
	requireRejected := func(value, phase string) {
		t.Helper()
		err := connect(value)
		control()
		if !postgresAuthenticationRejected(err) {
			t.Fatalf("%s: expected PostgreSQL password authentication rejection (28P01), got %T", phase, err)
		}
	}
	issue := func() (secrets.Lease, string) {
		t.Helper()
		lease, err := broker.IssueFor(ctx, run, capability.Grant{OrgID: org}, "PG_LOGIN", "staging", 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		value, err := broker.Redeem(secrets.WithRunID(ctx, run), lease.Token)
		if err != nil {
			t.Fatal(err)
		}
		var credential struct{ Username string }
		if err := json.Unmarshal([]byte(value), &credential); err != nil {
			t.Fatal(err)
		}
		issuedUsers = append(issuedUsers, credential.Username)
		if err := connect(value); err != nil {
			t.Fatal("issued credential did not authenticate to PostgreSQL")
		}
		return lease, value
	}
	// Provider TTL is three seconds, beyond this one-second grant but below
	// the ten-second request. Rejection must actually drop the issued PG role.
	if _, err := broker.IssueFor(ctx, run, capability.Grant{OrgID: org, ExpiresAt: time.Now().Add(time.Second)}, "PG_LOGIN", "staging", 10*time.Second); !errors.Is(err, secrets.ErrProviderContract) {
		t.Fatalf("grant-overlong provider lease accepted: %v", err)
	}
	control()
	var remaining int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_roles WHERE starts_with(rolname,$1)", rolePrefix).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("grant-overlong provider lease not actually revoked: remaining=%d err=%v", remaining, err)
	}
	t.Log("provider lease between grant and requested deadline refused; issued PostgreSQL role removed")
	lease, value := issue()
	if err := broker.Revoke(ctx, lease.ID); err != nil {
		t.Fatal(err)
	}
	requireRejected(value, "provider revocation")
	lease, value = issue()
	time.Sleep(time.Until(lease.ExpiresAt) + 150*time.Millisecond)
	requireRejected(value, "healthy-provider expiry")
	t.Log("real OpenBao dynamic PostgreSQL credential authenticated; revoked and healthy-provider expired credentials rejected by PostgreSQL")
	lease, value = issue()
	var credential struct{ Username string }
	if err := json.Unmarshal([]byte(value), &credential); err != nil {
		t.Fatal(err)
	}
	var hardExpiry time.Time
	if err := pool.QueryRow(ctx, "SELECT rolvaliduntil FROM pg_roles WHERE rolname=$1", credential.Username).Scan(&hardExpiry); err != nil {
		t.Fatal(err)
	}
	t.Logf("provider lease expiry=%s PostgreSQL VALID UNTIL=%s delta=%s", lease.ExpiresAt.UTC().Format(time.RFC3339Nano), hardExpiry.UTC().Format(time.RFC3339Nano), hardExpiry.Sub(lease.ExpiresAt))
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(lease.ExpiresAt) + 150*time.Millisecond)
	outageErr := connect(value)
	control()
	if outageErr == nil {
		t.Log("OBSERVED LIMITATION: PostgreSQL credential remains usable past OpenBao lease expiry while provider is stopped")
	} else if postgresAuthenticationRejected(outageErr) {
		t.Log("PostgreSQL independently refused credential at provider lease expiry (28P01)")
	} else {
		t.Fatalf("outage probe inconclusive: non-authentication error %T", outageErr)
	}
	if time.Until(hardExpiry) > 15*time.Second {
		t.Fatal("unexpected target expiry beyond owned fixture ceiling")
	}
	time.Sleep(max(0, time.Until(hardExpiry)) + 150*time.Millisecond)
	requireRejected(value, "target VALID UNTIL")
	t.Log("PostgreSQL independently enforces its later VALID UNTIL; OpenBao lease expiry is not an outage-safe target cutoff")
}

// Authentication failures alone prove rejection; network and availability
// errors must never turn this enforcement test green.
func postgresAuthenticationRejected(err error) bool {
	var pgerr *pgconn.PgError
	return errors.As(err, &pgerr) && pgerr.Code == "28P01"
}

func TestPostgresRejectionEvidenceRejectsTransportErrors(t *testing.T) {
	for _, err := range []error{nil, context.DeadlineExceeded, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("unreachable")}, &pgconn.PgError{Code: "57P03"}} {
		if postgresAuthenticationRejected(err) {
			t.Errorf("non-authentication error accepted as revocation evidence: %T", err)
		}
	}
	if !postgresAuthenticationRejected(fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "28P01"})) {
		t.Error("explicit password rejection not recognized")
	}
}
