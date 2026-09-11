package identity

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// UserByID returns one user. It exists so the API can answer "who am I"
// without another service reading the identity schema.
func (s *Store) UserByID(ctx context.Context, id uuid.UUID) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, username, password_hash, totp_secret, created_at
		   FROM identity.users WHERE id = $1`, id).
		Scan(&u.ID, &u.Email, &u.Username, &u.PasswordHash, &u.TOTPSecret, &u.CreatedAt)
	if err != nil {
		return User{}, fmt.Errorf("user %s not found: %w", id, err)
	}
	return u, nil
}

// ListOrgsForUser returns the organizations a user belongs to. A user who
// belongs to none gets an empty list, never every organization.
func (s *Store) ListOrgsForUser(ctx context.Context, userID uuid.UUID) ([]Org, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT o.id, o.name, o.created_at
		   FROM identity.organizations o
		   JOIN identity.org_members m ON m.org_id = o.id
		  WHERE m.user_id = $1
		  ORDER BY o.name`, userID)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	defer rows.Close()

	out := []Org{}
	for rows.Next() {
		var o Org
		if err := rows.Scan(&o.ID, &o.Name, &o.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan organization: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Member is one organization membership.
type Member struct {
	UserID   uuid.UUID
	Username string
	Role     string
}

// ListOrgMembers returns the members of an organization.
func (s *Store) ListOrgMembers(ctx context.Context, orgID uuid.UUID) ([]Member, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT u.id, u.username, m.role
		   FROM identity.org_members m
		   JOIN identity.users u ON u.id = m.user_id
		  WHERE m.org_id = $1
		  ORDER BY u.username`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Username, &m.Role); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListSSHKeys returns a user's registered public keys. The key material is
// public by definition; the private half never reaches the platform.
func (s *SSHKeyStore) List(ctx context.Context, userID uuid.UUID) ([]SSHKey, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, title, fingerprint, public_key, created_at
		   FROM identity.ssh_keys WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("list ssh keys: %w", err)
	}
	defer rows.Close()

	out := []SSHKey{}
	for rows.Next() {
		var k SSHKey
		if err := rows.Scan(&k.ID, &k.UserID, &k.Title, &k.Fingerprint, &k.PublicKey, &k.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan ssh key: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Delete removes a key, but only the owner's own key: the user id is part of
// the predicate rather than checked afterwards.
func (s *SSHKeyStore) Delete(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM identity.ssh_keys WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete ssh key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("ssh key %s not found for this user", id)
	}
	return nil
}

// List returns a user's access tokens. The plaintext is never stored, so it is
// never listed — only the metadata.
func (s *TokenStore) List(ctx context.Context, userID uuid.UUID) ([]Token, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, name, scopes, expires_at, revoked_at
		   FROM identity.access_tokens
		  WHERE user_id = $1 AND revoked_at IS NULL
		  ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer rows.Close()

	out := []Token{}
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Scopes, &t.ExpiresAt, &t.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan token: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
