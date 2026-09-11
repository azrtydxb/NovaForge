package identity_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/identity"
)

// These pin a defect that reached a live cluster: the org lookup queried an
// unqualified table name, so it failed at runtime with "relation
// organizations does not exist" while every existing test still passed,
// because nothing exercised this path.
func TestOrgByNameOrIDResolvesBothSpellings(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	owner := mustUser(t, st, "orgref")
	name := "orgref-" + uuid.NewString()[:8]
	created, err := st.CreateOrg(ctx, name, owner)
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	byName, err := st.OrgByNameOrID(ctx, name)
	if err != nil {
		t.Fatalf("OrgByNameOrID(name): %v", err)
	}
	if byName.ID != created.ID {
		t.Fatalf("by name: want %s, got %s", created.ID, byName.ID)
	}

	byID, err := st.OrgByNameOrID(ctx, created.ID.String())
	if err != nil {
		t.Fatalf("OrgByNameOrID(id): %v", err)
	}
	if byID.Name != name {
		t.Fatalf("by id: want %s, got %s", name, byID.Name)
	}

	if _, err := st.OrgByNameOrID(ctx, "no-such-org-"+uuid.NewString()[:8]); err == nil {
		t.Fatal("want an error for an unknown organization")
	}
}

func TestResolveOrgScopeRefusesNonMember(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	owner := mustUser(t, st, "member")
	outsider := mustUser(t, st, "outsider")
	name := "scoped-" + uuid.NewString()[:8]
	if _, err := st.CreateOrg(ctx, name, owner); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	if _, err := st.ResolveOrgScope(ctx, owner, name); err != nil {
		t.Fatalf("the owner must resolve their own org: %v", err)
	}

	_, err := st.ResolveOrgScope(ctx, outsider, name)
	if err == nil {
		t.Fatal("a non-member must be refused, not given an empty scope")
	}
	if !strings.Contains(err.Error(), "not a member") {
		t.Fatalf("want 'not a member' in the error, got %v", err)
	}
}

// mustUser creates a user with a unique name and returns its id.
func mustUser(t *testing.T, st *identity.Store, prefix string) uuid.UUID {
	t.Helper()
	suffix := uuid.NewString()[:8]
	u, err := st.CreateUser(context.Background(),
		prefix+suffix+"@example.com", prefix+suffix, "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u.ID
}
