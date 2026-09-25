package webhooks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/webhooks"
)

// testKEK is the key-encryption key these tests store hook secrets under. It is
// a test value, not a default: a store built with an empty KEK refuses to hold a
// secret rather than storing one in the clear.
var testKEK = []byte("webhook-tests-kek")

// suite is the database this package's tests share: one throwaway PostgreSQL
// database, created by TestMain and dropped after.
//
// It is not the cluster's dev database itself, on purpose. That one is shared
// with everything else being built against this cluster, and its
// gitplatform_git tracking table already recorded a migration version this
// worktree's migration set does not contain — golang-migrate then refuses the
// whole set ("no migration found for version 4"), which has nothing to do with
// the code under test. A database of our own also means these tests cannot see,
// or be seen by, another lane's rows.
var suite struct {
	url  string
	pool *pgxpool.Pool
}

func TestMain(m *testing.M) {
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		// Without a datastore every test in the package skips. That is
		// indistinguishable from a pass in the output, which is why hack/env.sh
		// says so out loud when it cannot discover one.
		os.Exit(m.Run())
	}
	code, err := withDatabase(admin, m)
	if err != nil {
		panic(err)
	}
	os.Exit(code)
}

func withDatabase(admin string, m *testing.M) (int, error) {
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, admin)
	if err != nil {
		return 0, fmt.Errorf("connect to create a test database: %w", err)
	}
	defer owner.Close(ctx)

	name := "hooks_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := owner.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		return 0, fmt.Errorf("create test database: %w", err)
	}
	defer func() { _, _ = owner.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)") }()

	u, err := url.Parse(admin)
	if err != nil {
		return 0, fmt.Errorf("parse TEST_DATABASE_URL: %w", err)
	}
	u.Path = "/" + name
	suite.url = u.String()

	// The gitplatform (repositories) set comes first because hooks reference a
	// repository: the cascade is what removes a deleted repository's hooks, so
	// the order is a real dependency and not test convenience.
	if err := database.MigrateAs(suite.url, "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		return 0, fmt.Errorf("migrate gitplatform (repositories): %w", err)
	}
	if err := database.MigrateAs(suite.url, "gitplatform", "gitplatform_webhooks", webhooks.MigrationsFS); err != nil {
		return 0, fmt.Errorf("migrate gitplatform (webhooks): %w", err)
	}
	pool, err := database.Connect(ctx, suite.url)
	if err != nil {
		return 0, fmt.Errorf("connect: %w", err)
	}
	suite.pool = pool
	defer pool.Close()
	return m.Run(), nil
}

func webhooksPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if suite.pool == nil {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return suite.pool
}

// mustRepoRow inserts a repository row directly. These tests are about hooks,
// not about repository creation, and creating one through the RPC would drag in
// a filesystem root they have no use for.
func mustRepoRow(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		`INSERT INTO gitplatform.repositories (id, org_id, name, default_branch)
		 VALUES ($1, $2, $3, 'main') RETURNING id`,
		uuid.New(), orgID, "hooks-"+uuid.NewString()[:8]).Scan(&id)
	if err != nil {
		t.Fatalf("insert repository: %v", err)
	}
	return id
}

// personIn is the scope a member of orgID acts under. Every store method takes
// its organization from this and never from an argument, so a caller cannot name
// another organization and be believed.
func personIn(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID: orgID, ActorID: uuid.New(), ActorKind: "user", Role: "admin",
	})
}

// TestWebhookSecretNeverReadBack is the property that makes a webhook secret
// worth anything: it is write-only. A secret can be set at creation and rotated
// afterwards, and neither value comes back out of any read — not from the hook
// listing, not from the delivery history, and not from the column itself, which
// holds ciphertext.
func TestWebhookSecretNeverReadBack(t *testing.T) {
	pool := webhooksPool(t)
	orgID := uuid.New()
	repoID := mustRepoRow(t, pool, orgID)
	ctx := personIn(orgID)
	store := webhooks.NewStore(pool, testKEK)

	first := "first-secret-" + uuid.NewString()
	second := "rotated-secret-" + uuid.NewString()

	hook, err := store.CreateHook(ctx, repoID, "https://example.invalid/hook", []string{"push"}, first)
	if err != nil {
		t.Fatalf("CreateHook: %v", err)
	}
	if !hook.HasSecret {
		t.Fatal("a hook created with a secret reports it has none")
	}
	if err := store.SetSecret(ctx, hook.ID, second); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}

	// Everything a reader can obtain, marshalled: a field added later that
	// carried the secret would fail this without anyone having to remember to
	// extend the test.
	listed, err := store.ListHooks(ctx, repoID)
	if err != nil {
		t.Fatalf("ListHooks: %v", err)
	}
	deliveries, err := store.ListDeliveries(ctx, hook.ID, 50)
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	readable, err := json.Marshal(map[string]any{"hook": hook, "hooks": listed, "deliveries": deliveries})
	if err != nil {
		t.Fatalf("marshal reads: %v", err)
	}
	for _, secret := range []string{first, second} {
		if strings.Contains(string(readable), secret) {
			t.Fatalf("a read returned the secret %q: %s", secret, readable)
		}
	}

	// At rest it is ciphertext. A store that simply kept the bytes would pass
	// every assertion above while handing the secret to anyone with a database
	// connection.
	var stored []byte
	if err := pool.QueryRow(ctx,
		`SELECT secret FROM gitplatform.hooks WHERE id = $1`, hook.ID).Scan(&stored); err != nil {
		t.Fatalf("read stored secret: %v", err)
	}
	if len(stored) == 0 {
		t.Fatal("the rotated secret was not stored at all")
	}
	for _, secret := range []string{first, second} {
		if strings.Contains(string(stored), secret) {
			t.Fatalf("the secret is stored in the clear: %q", stored)
		}
	}
}

// TestHooksAreScopedToTheirOrganization is the boundary: a hook belongs to one
// organization's repository, and a scope for another organization neither sees it
// nor can change it — even naming its repository and hook ids exactly.
func TestHooksAreScopedToTheirOrganization(t *testing.T) {
	pool := webhooksPool(t)
	store := webhooks.NewStore(pool, testKEK)
	orgID, otherOrg := uuid.New(), uuid.New()
	repoID := mustRepoRow(t, pool, orgID)

	hook, err := store.CreateHook(personIn(orgID), repoID, "https://example.invalid/hook", nil, "s")
	if err != nil {
		t.Fatalf("CreateHook: %v", err)
	}

	intruder := personIn(otherOrg)
	if listed, err := store.ListHooks(intruder, repoID); err == nil && len(listed) != 0 {
		t.Fatalf("another organization listed %d hooks", len(listed))
	}
	if _, err := store.CreateHook(intruder, repoID, "https://example.invalid/theirs", nil, ""); err == nil {
		t.Fatal("another organization created a hook on this repository")
	}
	if err := store.SetSecret(intruder, hook.ID, "theirs"); err == nil {
		t.Fatal("another organization rotated this hook's secret")
	}
	if err := store.DeleteHook(intruder, hook.ID); err == nil {
		t.Fatal("another organization deleted this hook")
	}
	if _, err := store.ListDeliveries(intruder, hook.ID, 10); err == nil {
		t.Fatal("another organization read this hook's delivery history")
	}

	// And an unscoped context is refused rather than treated as unrestricted.
	if _, err := store.ListHooks(context.Background(), repoID); err == nil {
		t.Fatal("a context with no scope listed hooks")
	}
}

// TestAnAgentMayNotRegisterAWebhook records a deliberate refusal: a webhook is a
// standing instruction to send everything a push carries to an address outside
// the platform, and no capability grant expresses that. Agents write code; they
// do not configure egress.
func TestAnAgentMayNotRegisterAWebhook(t *testing.T) {
	pool := webhooksPool(t)
	store := webhooks.NewStore(pool, testKEK)
	orgID := uuid.New()
	repoID := mustRepoRow(t, pool, orgID)

	agent := authz.WithScope(context.Background(), authz.Scope{
		OrgID: orgID, ActorID: uuid.New(), ActorKind: "agent",
	})
	if _, err := store.CreateHook(agent, repoID, "https://exfiltrate.invalid/", nil, ""); err == nil {
		t.Fatal("an agent registered a webhook")
	}
}
