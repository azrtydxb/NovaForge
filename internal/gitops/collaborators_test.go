package gitops_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gitops"
)

// A person who is not a member of an organization can be granted one repository in
// it and reaches only that repository. Grants may name a team as well as a person,
// which is what makes teams load-bearing rather than decorative.
//
// Teams live in Identity's schema, which git-platform may not read, so the team
// lookup is a function the store is given. Here it is a stub of that lookup, not of
// the grants themselves: the grants, the uniqueness, the role resolution and the
// organization predicate are all real.
func TestRepositoryCollaboratorGrants(t *testing.T) {
	pool := gitopsPool(t)
	orgID := uuid.New()
	outsider := uuid.New()
	teamID := uuid.New()

	granted := mustRepoRow(t, pool, orgID, "granted")
	other := mustRepoRow(t, pool, orgID, "other")

	inTeam := func(ctx context.Context, org, user uuid.UUID) ([]uuid.UUID, error) {
		if org == orgID && user == outsider {
			return []uuid.UUID{teamID}, nil
		}
		return nil, nil
	}
	store := gitops.NewCollaboratorStore(pool, inTeam)
	ctx := context.Background()

	t.Run("no grant reaches nothing", func(t *testing.T) {
		access, err := store.ReposForUser(ctx, orgID, outsider)
		if err != nil {
			t.Fatalf("ReposForUser: %v", err)
		}
		if len(access) != 0 {
			t.Fatalf("a person with no grant reaches %+v", access)
		}
	})

	t.Run("a direct grant reaches exactly that repository", func(t *testing.T) {
		if err := store.AddCollaborator(ctx, orgID, granted, outsider, uuid.Nil, "write"); err != nil {
			t.Fatalf("AddCollaborator: %v", err)
		}
		role, ok, err := store.MayAccess(ctx, orgID, granted, outsider)
		if err != nil || !ok || role != "write" {
			t.Fatalf("MayAccess granted = %q, %v, %v; want write, true, nil", role, ok, err)
		}
		if _, ok, _ := store.MayAccess(ctx, orgID, other, outsider); ok {
			t.Fatal("the grant reached a second repository in the same organization")
		}
	})

	t.Run("granting twice is not an error", func(t *testing.T) {
		if err := store.AddCollaborator(ctx, orgID, granted, outsider, uuid.Nil, "write"); err != nil {
			t.Fatalf("a repeated grant failed: %v", err)
		}
		list, err := store.ListCollaborators(ctx, orgID, granted)
		if err != nil {
			t.Fatalf("ListCollaborators: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("a repeated grant produced %d rows, want 1", len(list))
		}
	})

	t.Run("another organization's repository cannot be granted", func(t *testing.T) {
		elsewhere := uuid.New()
		err := store.AddCollaborator(ctx, elsewhere, granted, outsider, uuid.Nil, "write")
		if err == nil {
			t.Fatal("a grant was accepted for a repository in another organization")
		}
	})

	t.Run("a team grant reaches the team's members", func(t *testing.T) {
		if err := store.AddCollaborator(ctx, orgID, other, uuid.Nil, teamID, "read"); err != nil {
			t.Fatalf("AddCollaborator team: %v", err)
		}
		role, ok, err := store.MayAccess(ctx, orgID, other, outsider)
		if err != nil || !ok || role != "read" {
			t.Fatalf("a team grant gave %q, %v, %v; want read, true, nil", role, ok, err)
		}
		// Someone not in the team is unaffected by it.
		stranger := uuid.New()
		if _, ok, _ := store.MayAccess(ctx, orgID, other, stranger); ok {
			t.Fatal("a team grant reached someone who is not in the team")
		}
	})

	t.Run("the stronger of two grants wins", func(t *testing.T) {
		// Read directly and write through a team is write: the weaker grant does
		// not narrow the stronger one.
		if err := store.AddCollaborator(ctx, orgID, other, outsider, uuid.Nil, "read"); err != nil {
			t.Fatalf("AddCollaborator direct read: %v", err)
		}
		if err := store.RemoveCollaborator(ctx, orgID, other, uuid.Nil, teamID); err != nil {
			t.Fatalf("RemoveCollaborator team: %v", err)
		}
		if err := store.AddCollaborator(ctx, orgID, other, uuid.Nil, teamID, "write"); err != nil {
			t.Fatalf("AddCollaborator team write: %v", err)
		}
		role, _, err := store.MayAccess(ctx, orgID, other, outsider)
		if err != nil {
			t.Fatal(err)
		}
		if role != "write" {
			t.Fatalf("read directly and write by team resolved to %q, want write", role)
		}
	})

	t.Run("revoking removes the access", func(t *testing.T) {
		if err := store.RemoveCollaborator(ctx, orgID, granted, outsider, uuid.Nil); err != nil {
			t.Fatalf("RemoveCollaborator: %v", err)
		}
		if _, ok, _ := store.MayAccess(ctx, orgID, granted, outsider); ok {
			t.Fatal("a revoked grant still reaches the repository")
		}
	})

	t.Run("a grant names exactly one subject", func(t *testing.T) {
		if err := store.AddCollaborator(ctx, orgID, granted, outsider, teamID, "write"); err == nil {
			t.Fatal("a grant naming both a person and a team was accepted")
		}
		if err := store.AddCollaborator(ctx, orgID, granted, uuid.Nil, uuid.Nil, "write"); err == nil {
			t.Fatal("a grant naming neither a person nor a team was accepted")
		}
	})

	t.Run("an unavailable team lookup is an error, not an empty answer", func(t *testing.T) {
		// Reporting "no repositories" when the answer is unknown would look like a
		// revoked grant and hide an outage.
		failing := gitops.NewCollaboratorStore(pool, func(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
			return nil, context.DeadlineExceeded
		})
		if _, err := failing.ReposForUser(ctx, orgID, outsider); err == nil {
			t.Fatal("an unavailable team lookup reported an empty set instead of failing")
		}
	})
}

// gitopsPool migrates the schema and returns a pool, for tests that need the
// tables without the RPC server.
func gitopsPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := gitopsDBURL(t)
	if err := database.MigrateAs(url, "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform schema: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// mustRepoRow inserts a repository row directly. These tests are about grants, not
// about repository creation, and creating one through the RPC would drag in a
// filesystem root this test has no use for.
func mustRepoRow(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		`INSERT INTO gitplatform.repositories (id, org_id, name, default_branch) VALUES ($1, $2, $3, 'main') RETURNING id`,
		uuid.New(), orgID, name+"-"+uuid.NewString()[:8]).Scan(&id)
	if err != nil {
		t.Fatalf("insert repository: %v", err)
	}
	return id
}
