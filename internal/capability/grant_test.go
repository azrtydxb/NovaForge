package capability_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
)

func TestWriteBranchScopeEnforced(t *testing.T) {
	g := capability.Grant{WriteBranch: "agents/NF-182/"}
	if err := capability.CanWriteRef(g, "refs/heads/agents/NF-182/work"); err != nil {
		t.Fatalf("want allowed, got %v", err)
	}
	err := capability.CanWriteRef(g, "refs/heads/main")
	if err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("want error containing 'not permitted', got %v", err)
	}
}

func TestEmptyWriteBranchDeniesAll(t *testing.T) {
	g := capability.Grant{}
	if err := capability.CanWriteRef(g, "refs/heads/main"); err == nil {
		t.Fatal("want error for empty write branch")
	}
}

func TestPrefixEscapeDenied(t *testing.T) {
	g := capability.Grant{WriteBranch: "agents/NF-1/"}
	if err := capability.CanWriteRef(g, "refs/heads/agents/NF-10/x"); err == nil {
		t.Fatal("want error for prefix escape")
	}
}

func TestIssueAndResolve(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := database.Migrate(url, "gitplatform", os.DirFS("migrations")); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	store := capability.NewStore(pool)

	g := capability.Grant{
		OrgID:       uuid.New(),
		SubjectID:   uuid.New(),
		SubjectKind: "agent",
		RepoRead:    true,
		WriteBranch: "agents/NF-1/",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	issued, err := store.Issue(ctx, g)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if issued.ID == uuid.Nil {
		t.Fatal("want issued grant to have an id")
	}

	resolved, err := store.Resolve(ctx, issued.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.WriteBranch != g.WriteBranch {
		t.Fatalf("want write branch %q, got %q", g.WriteBranch, resolved.WriteBranch)
	}
}

func TestResolveExpiredGrant(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := database.Migrate(url, "gitplatform", os.DirFS("migrations")); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	store := capability.NewStore(pool)

	g := capability.Grant{
		OrgID:       uuid.New(),
		SubjectID:   uuid.New(),
		SubjectKind: "user",
		ExpiresAt:   time.Now().Add(-time.Hour),
	}
	issued, err := store.Issue(ctx, g)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := store.Resolve(ctx, issued.ID); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("want error containing 'expired', got %v", err)
	}
}
