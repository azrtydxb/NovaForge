package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
)

// RelationshipManifest schema 1 is repository documentation, not operational
// evidence, authorization or absence proof. All keys are local to this manifest.
type RelationshipManifest struct {
	Schema   int              `json:"schema"`
	Entities []DeclaredEntity `json:"entities"`
	Edges    []DeclaredEdge   `json:"edges"`
}
type DeclaredEntity struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}
type DeclaredEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

var declaredKey = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,127}$`)

func ParseRelationshipManifest(data []byte) (RelationshipManifest, error) {
	var m RelationshipManifest
	if len(data) > 1<<20 {
		return m, fmt.Errorf("relationship manifest exceeds 1MiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return m, fmt.Errorf("trailing relationship manifest")
	}
	if m.Schema != 1 || len(m.Entities) > 1000 || len(m.Edges) > 5000 {
		return m, fmt.Errorf("invalid relationship schema or bounds")
	}
	kinds := map[string]string{}
	for _, e := range m.Entities {
		if !declaredKey.MatchString(e.Key) || e.Name == "" || len(e.Name) > 512 || kinds[e.Key] != "" {
			return m, fmt.Errorf("invalid or duplicate entity")
		}
		switch e.Kind {
		case "service", "api", "schema", "work_item", "commit", "adr", "deployment", "owner", "incident":
		default:
			return m, fmt.Errorf("entity kind %q is not declarative metadata", e.Kind)
		}
		kinds[e.Key] = e.Kind
	}
	for _, e := range m.Edges {
		from, to := kinds[e.From], kinds[e.To]
		if from == "" || to == "" || e.From == e.To {
			return m, fmt.Errorf("unresolved or reflexive relationship")
		}
		valid := false
		switch e.Kind {
		case "owned_by":
			valid = to == "owner" && from != "owner"
		case "implements":
			valid = (to == "adr" || to == "work_item" || to == "api") && (from == "service" || from == "api" || from == "schema")
		case "depends_on", "called_by":
			valid = (from == "service" || from == "api" || from == "schema") && (to == "service" || to == "api" || to == "schema")
		case "changed_by":
			valid = to == "commit" || to == "work_item"
		}
		if !valid {
			return m, fmt.Errorf("invalid declared relationship %s: %s -> %s", e.Kind, from, to)
		}
	}
	return m, nil
}

// ReplaceDeclaredRelationships is called by the indexer at the pinned revision.
// A missing manifest removes prior declarations. A malformed one preserves the
// previous generation but fails the checkpoint, rather than publishing emptiness.
func (s *Store) ReplaceDeclaredRelationships(ctx context.Context, org, repo uuid.UUID, revision string, data []byte) error {
	if err := authz.RequireOrg(ctx, org); err != nil {
		return err
	}
	var m RelationshipManifest
	var err error
	if data != nil {
		m, err = ParseRelationshipManifest(data)
		if err != nil {
			return err
		}
	}
	tx, err := beginIndexWrite(ctx, s.pool, org, repo)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND attrs->>'provenance'='declared'`, org, repo); err != nil {
		return err
	}
	ids := map[string]uuid.UUID{}
	for _, e := range m.Entities {
		n, err := upsertNode(ctx, tx, org, repo, true, Node{OrgID: org, Kind: e.Kind, Key: repo.String() + ":declared:" + e.Key, Attrs: map[string]string{"name": e.Name, "provenance": "declared", "revision": revision, "manifest_hash": SourceDigest(data), "absence_safe": "false"}})
		if err != nil {
			return err
		}
		ids[e.Key] = n.ID
	}
	for _, e := range m.Edges {
		if err := upsertEdge(ctx, tx, Edge{FromID: ids[e.From], ToID: ids[e.To], Kind: e.Kind}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
