package graph_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/graph"
)

func TestEdgeWritesRequireBothEndpointsInCallerOrganization(t *testing.T) {
	store := newStore(t)
	orgA, orgB := uuid.New(), uuid.New()
	ctxA, ctxB := scopedCtx(orgA, uuid.New()), scopedCtx(orgB, uuid.New())
	node := func(ctx context.Context, org uuid.UUID) graph.Node {
		n, err := store.UpsertNode(ctx, graph.Node{OrgID: org, Kind: "symbol", Key: uuid.NewString()})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	a1, a2, b1, b2 := node(ctxA, orgA), node(ctxA, orgA), node(ctxB, orgB), node(ctxB, orgB)
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		from, to uuid.UUID
		allowed  bool
	}{
		{"owned", ctxA, a1.ID, a2.ID, true},
		{"foreign source", ctxA, b1.ID, a1.ID, false},
		{"foreign target", ctxA, a1.ID, b1.ID, false},
		{"both foreign", ctxA, b1.ID, b2.ID, false},
		{"no scope", context.Background(), a1.ID, a2.ID, false},
		{"platform worker", authz.WithScope(context.Background(), authz.Scope{ActorKind: "service", PlatformWorker: "index"}), a1.ID, a2.ID, false},
		{"missing", ctxA, a1.ID, uuid.New(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edge := graph.Edge{FromID: tc.from, ToID: tc.to, Kind: "depends_on"}
			for i := 0; i < 2; i++ {
				err := store.UpsertEdge(tc.ctx, edge)
				if (err == nil) != tc.allowed {
					t.Errorf("write %d: err=%v, allowed=%v", i, err, tc.allowed)
				}
			}
			if !tc.allowed && tc.name != "no scope" && tc.name != "platform worker" {
				var count int
				if err := store.Pool().QueryRow(context.Background(), `SELECT count(*) FROM graph.graph_edges WHERE from_id=$1 AND to_id=$2`, tc.from, tc.to).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Errorf("unauthorized edge persisted: %d", count)
				}
			}
		})
	}
}

func TestReplaceSubgraphRejectsForeignEdgeAndRollsBack(t *testing.T) {
	store := newStore(t)
	orgA, orgB, repo := uuid.New(), uuid.New(), uuid.New()
	ctxA, ctxB := scopedCtx(orgA, uuid.New()), scopedCtx(orgB, uuid.New())
	path := "pkg/code.go"
	original := graph.Node{ID: uuid.New(), OrgID: orgA, Kind: "symbol", Key: uuid.NewString(), Attrs: map[string]string{"path": path}}
	if err := store.ReplaceFileSubgraph(ctxA, orgA, repo, path, []graph.Node{original}, nil); err != nil {
		t.Fatal(err)
	}
	foreign, err := store.UpsertNode(ctxB, graph.Node{OrgID: orgB, Kind: "symbol", Key: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	replacement := original
	replacement.ID = uuid.New()
	err = store.ReplaceFileSubgraph(ctxA, orgA, repo, path, []graph.Node{replacement}, []graph.Edge{{FromID: replacement.ID, ToID: foreign.ID, Kind: "depends_on"}})
	if err == nil {
		t.Error("replacement accepted foreign edge")
	}
	var id uuid.UUID
	if err := store.Pool().QueryRow(ctxA, `SELECT id FROM graph.graph_nodes WHERE org_id=$1 AND key=$2`, orgA, original.Key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != original.ID {
		t.Errorf("failed replacement changed original %s to %s", original.ID, id)
	}
}
