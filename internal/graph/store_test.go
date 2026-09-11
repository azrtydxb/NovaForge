package graph_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/graph"
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
	if err := database.Migrate(url, "graph", os.DirFS("migrations")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newStore(t *testing.T) *graph.Store {
	t.Helper()
	return graph.NewStore(storePool(t))
}

func scopedCtx(orgID, actorID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{OrgID: orgID, ActorID: actorID, ActorKind: "user"})
}

func TestNeighboursFollowsEdgeDirection(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())

	apiNode := graph.Node{ID: uuid.New(), OrgID: orgID, Kind: "service", Key: uniqueKey("api")}
	userService := graph.Node{ID: uuid.New(), OrgID: orgID, Kind: "symbol", Key: uniqueKey("UserService")}

	if _, err := store.UpsertNode(ctx, apiNode); err != nil {
		t.Fatalf("UpsertNode api: %v", err)
	}
	if _, err := store.UpsertNode(ctx, userService); err != nil {
		t.Fatalf("UpsertNode userService: %v", err)
	}
	if err := store.UpsertEdge(ctx, graph.Edge{FromID: apiNode.ID, ToID: userService.ID, Kind: "depends_on"}); err != nil {
		t.Fatalf("UpsertEdge: %v", err)
	}

	inbound, err := store.Neighbours(ctx, userService.ID, "depends_on", "in")
	if err != nil {
		t.Fatalf("Neighbours in: %v", err)
	}
	if len(inbound) != 1 || inbound[0].ID != apiNode.ID {
		t.Fatalf("inbound = %+v, want [api]", inbound)
	}

	outbound, err := store.Neighbours(ctx, userService.ID, "depends_on", "out")
	if err != nil {
		t.Fatalf("Neighbours out: %v", err)
	}
	if len(outbound) != 0 {
		t.Fatalf("outbound = %+v, want none", outbound)
	}
}

func TestUpsertNodeIsIdempotent(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())
	key := uniqueKey("dup")

	first, err := store.UpsertNode(ctx, graph.Node{ID: uuid.New(), OrgID: orgID, Kind: "file", Key: key, Attrs: map[string]string{"v": "1"}})
	if err != nil {
		t.Fatalf("UpsertNode 1: %v", err)
	}
	second, err := store.UpsertNode(ctx, graph.Node{ID: uuid.New(), OrgID: orgID, Kind: "file", Key: key, Attrs: map[string]string{"v": "2"}})
	if err != nil {
		t.Fatalf("UpsertNode 2: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected same row id, got %s and %s", first.ID, second.ID)
	}
	if second.Attrs["v"] != "2" {
		t.Fatalf("expected second call's attrs, got %+v", second.Attrs)
	}

	got, err := store.Neighbours(ctx, second.ID, "depends_on", "out")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("unexpected neighbours: %+v", got)
	}
}

func TestReplaceFileSubgraphRemovesStaleSymbols(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())
	path := "internal/foo/foo.go"

	a := graph.Node{ID: uuid.New(), OrgID: orgID, Kind: "symbol", Key: uniqueKey("A"), Attrs: map[string]string{"path": path, "name": "A"}}
	b := graph.Node{ID: uuid.New(), OrgID: orgID, Kind: "symbol", Key: uniqueKey("B"), Attrs: map[string]string{"path": path, "name": "B"}}

	if err := store.ReplaceFileSubgraph(ctx, orgID, repoID, path, []graph.Node{a, b}, nil); err != nil {
		t.Fatalf("ReplaceFileSubgraph 1: %v", err)
	}

	inA, err := store.Neighbours(ctx, a.ID, "depends_on", "out")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}
	_ = inA

	a2 := graph.Node{ID: uuid.New(), OrgID: orgID, Kind: "symbol", Key: a.Key, Attrs: map[string]string{"path": path, "name": "A"}}
	if err := store.ReplaceFileSubgraph(ctx, orgID, repoID, path, []graph.Node{a2}, nil); err != nil {
		t.Fatalf("ReplaceFileSubgraph 2: %v", err)
	}

	rows, err := store.Pool().Query(ctx, `SELECT key FROM graph.graph_nodes WHERE org_id = $1 AND kind = 'symbol' AND attrs->>'path' = $2`, orgID, path)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scan: %v", err)
		}
		keys = append(keys, k)
	}
	if len(keys) != 1 || keys[0] != a.Key {
		t.Fatalf("keys after re-index = %v, want only %s", keys, a.Key)
	}
}

func TestNeighboursIsOrgScoped(t *testing.T) {
	store := newStore(t)
	orgA := uuid.New()
	orgB := uuid.New()
	ctxA := scopedCtx(orgA, uuid.New())
	ctxB := scopedCtx(orgB, uuid.New())

	fromA := graph.Node{ID: uuid.New(), OrgID: orgA, Kind: "service", Key: uniqueKey("svcA")}
	toA := graph.Node{ID: uuid.New(), OrgID: orgA, Kind: "symbol", Key: uniqueKey("symA")}
	if _, err := store.UpsertNode(ctxA, fromA); err != nil {
		t.Fatalf("UpsertNode fromA: %v", err)
	}
	if _, err := store.UpsertNode(ctxA, toA); err != nil {
		t.Fatalf("UpsertNode toA: %v", err)
	}
	if err := store.UpsertEdge(ctxA, graph.Edge{FromID: fromA.ID, ToID: toA.ID, Kind: "depends_on"}); err != nil {
		t.Fatalf("UpsertEdge: %v", err)
	}

	// Querying the same node id from org B's scope must never traverse in.
	got, err := store.Neighbours(ctxB, toA.ID, "depends_on", "in")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("org B saw org A's neighbours: %+v", got)
	}
}

var keyCounter int

func uniqueKey(prefix string) string {
	keyCounter++
	return prefix + "-" + uuid.New().String()
}
