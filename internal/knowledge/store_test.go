package knowledge_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/knowledge"
)

func dbURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return u
}

func storePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := dbURL(t)
	if err := database.Migrate(url, "knowledge", os.DirFS("migrations")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newStore(t *testing.T) *knowledge.Store {
	t.Helper()
	return knowledge.NewStore(storePool(t))
}

func scopedCtx(orgID, actorID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{OrgID: orgID, ActorID: actorID, ActorKind: "user"})
}

func uniqueKey(prefix string) string {
	return prefix + "-" + uuid.New().String()
}

func unitVector(dim, hot int) []float32 {
	v := make([]float32, dim)
	v[hot] = 1
	return v
}

func TestRecordAndSearch(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())

	entry := knowledge.Entry{
		OrgID:  orgID,
		RepoID: repoID,
		Key:    uniqueKey("jwt-decision"),
		Kind:   "decision",
		Title:  "Never validate JWT tokens directly in route handlers",
		Body:   "Route handlers must call the auth middleware, which owns token validation, instead of parsing JWTs themselves.",
	}
	saved, err := store.Record(ctx, entry, unitVector(knowledge.EmbeddingDim, 7))
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if saved.ID == uuid.Nil {
		t.Fatal("Record did not assign an id")
	}

	got, err := store.Search(ctx, orgID, repoID, unitVector(knowledge.EmbeddingDim, 7), 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var found bool
	for _, e := range got {
		if e.ID == saved.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("Search did not return the recorded entry; got %+v", got)
	}
}

func TestSupersededEntryExcludedFromSearch(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())

	old, err := store.Record(ctx, knowledge.Entry{
		OrgID: orgID, RepoID: repoID, Key: uniqueKey("old-pattern"),
		Kind: "pattern", Title: "Old pattern", Body: "superseded body",
	}, unitVector(knowledge.EmbeddingDim, 9))
	if err != nil {
		t.Fatalf("Record old: %v", err)
	}
	newer, err := store.Record(ctx, knowledge.Entry{
		OrgID: orgID, RepoID: repoID, Key: uniqueKey("new-pattern"),
		Kind: "pattern", Title: "New pattern", Body: "replacement body",
	}, unitVector(knowledge.EmbeddingDim, 9))
	if err != nil {
		t.Fatalf("Record new: %v", err)
	}

	if err := store.Supersede(ctx, old.ID, newer.ID); err != nil {
		t.Fatalf("Supersede: %v", err)
	}

	results, err := store.Search(ctx, orgID, repoID, unitVector(knowledge.EmbeddingDim, 9), 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, e := range results {
		if e.ID == old.ID {
			t.Fatalf("Search returned superseded entry %+v", e)
		}
	}

	fetched, err := store.Get(ctx, old.ID)
	if err != nil {
		t.Fatalf("Get superseded entry: %v", err)
	}
	if fetched.ID != old.ID {
		t.Fatalf("Get returned wrong entry: %+v", fetched)
	}
}

func TestKnowledgeIsRepoScoped(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoA := uuid.New()
	repoB := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())

	v := unitVector(knowledge.EmbeddingDim, 11)
	_, err := store.Record(ctx, knowledge.Entry{
		OrgID: orgID, RepoID: repoB, Key: uniqueKey("repoB-entry"),
		Kind: "operational", Title: "Repo B only", Body: "belongs to repo B",
	}, v)
	if err != nil {
		t.Fatalf("Record repo B: %v", err)
	}

	got, err := store.Search(ctx, orgID, repoA, v, 10)
	if err != nil {
		t.Fatalf("Search repo A: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("repo A search returned repo B's entries: %+v", got)
	}
}

func TestCorrectionRecordsSourceRun(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())
	runID := uuid.New()

	saved, err := store.Record(ctx, knowledge.Entry{
		OrgID: orgID, RepoID: repoID, Key: uniqueKey("correction"),
		Kind: "correction", Title: "Fixed flaky retry loop", Body: "Root cause was...",
		SourceRunID: &runID,
	}, unitVector(knowledge.EmbeddingDim, 13))
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if saved.SourceRunID == nil || *saved.SourceRunID != runID {
		t.Fatalf("SourceRunID = %v, want %v", saved.SourceRunID, runID)
	}

	fetched, err := store.Get(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fetched.SourceRunID == nil || *fetched.SourceRunID != runID {
		t.Fatalf("Get SourceRunID = %v, want %v", fetched.SourceRunID, runID)
	}
	if fetched.Kind != "correction" {
		t.Fatalf("Kind = %q, want correction", fetched.Kind)
	}
}
