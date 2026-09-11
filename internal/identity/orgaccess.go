package identity

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// OrgByNameOrID resolves an organization from either its name or its id.
// Callers name organizations in URLs, while services carry ids, so both
// spellings reach this lookup.
func (s *Store) OrgByNameOrID(ctx context.Context, ref string) (Org, error) {
	if id, err := uuid.Parse(ref); err == nil {
		var o Org
		err := s.pool.QueryRow(ctx,
			`SELECT id, name FROM identity.organizations WHERE id = $1`, id).Scan(&o.ID, &o.Name)
		if err != nil {
			return Org{}, fmt.Errorf("organization %s not found: %w", ref, err)
		}
		return o, nil
	}
	var o Org
	err := s.pool.QueryRow(ctx,
		`SELECT id, name FROM identity.organizations WHERE name = $1`, ref).Scan(&o.ID, &o.Name)
	if err != nil {
		return Org{}, fmt.Errorf("organization %q not found: %w", ref, err)
	}
	return o, nil
}

// ResolveOrgScope returns the organization the user may act in, given the
// reference a caller supplied. A non-member is refused rather than silently
// given an empty scope, because an empty org scope must never read as
// "every organization".
func (s *Store) ResolveOrgScope(ctx context.Context, userID uuid.UUID, ref string) (Org, error) {
	o, err := s.OrgByNameOrID(ctx, ref)
	if err != nil {
		return Org{}, err
	}
	member, err := s.IsOrgMember(ctx, o.ID, userID)
	if err != nil {
		return Org{}, fmt.Errorf("check membership: %w", err)
	}
	if !member {
		return Org{}, fmt.Errorf("user %s is not a member of organization %q", userID, o.Name)
	}
	return o, nil
}
