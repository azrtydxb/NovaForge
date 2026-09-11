// Package identity owns the identity service's schema: users, organizations,
// org memberships, sessions, personal access tokens, TOTP secrets, and SSH
// keys. No other service reads these tables directly.
package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// User is a row in the identity.users table.
type User struct {
	ID           uuid.UUID
	Email        string
	Username     string
	PasswordHash string
	TOTPSecret   sql.NullString
	CreatedAt    time.Time
}

// Org is a row in the identity.organizations table.
type Org struct {
	ID        uuid.UUID
	Name      string
	CreatedAt time.Time
}

// Store provides access to the identity schema's core tables.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps pool as an identity.Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

const uniqueViolation = "23505"

// CreateUser inserts a new user row.
func (s *Store) CreateUser(ctx context.Context, email, username, passwordHash string) (User, error) {
	u := User{
		ID:           uuid.New(),
		Email:        email,
		Username:     username,
		PasswordHash: passwordHash,
	}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO identity.users (id, email, username, password_hash) VALUES ($1, $2, $3, $4)
		 RETURNING created_at`,
		u.ID, u.Email, u.Username, u.PasswordHash,
	).Scan(&u.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			if pgErr.ConstraintName == "users_username_key" {
				return User{}, fmt.Errorf("username %q already taken", username)
			}
			return User{}, fmt.Errorf("email %q already taken", email)
		}
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}

// UserByUsername looks up a user by (case-insensitive) username.
func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, username, password_hash, totp_secret, created_at
		 FROM identity.users WHERE username = $1`,
		username,
	).Scan(&u.ID, &u.Email, &u.Username, &u.PasswordHash, &u.TOTPSecret, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, fmt.Errorf("user %q not found: %w", username, err)
		}
		return User{}, fmt.Errorf("lookup user: %w", err)
	}
	return u, nil
}

// CreateOrg inserts a new organization and adds ownerID as its owner member.
func (s *Store) CreateOrg(ctx context.Context, name string, ownerID uuid.UUID) (Org, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Org{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	o := Org{ID: uuid.New(), Name: name}
	err = tx.QueryRow(ctx,
		`INSERT INTO identity.organizations (id, name) VALUES ($1, $2) RETURNING created_at`,
		o.ID, o.Name,
	).Scan(&o.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return Org{}, fmt.Errorf("organization %q already exists", name)
		}
		return Org{}, fmt.Errorf("create org: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO identity.org_members (org_id, user_id, role) VALUES ($1, $2, 'owner')`,
		o.ID, ownerID,
	); err != nil {
		return Org{}, fmt.Errorf("add owner member: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Org{}, fmt.Errorf("commit: %w", err)
	}
	return o, nil
}

// AddOrgMember adds userID to orgID with role.
func (s *Store) AddOrgMember(ctx context.Context, orgID, userID uuid.UUID, role string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO identity.org_members (org_id, user_id, role) VALUES ($1, $2, $3)`,
		orgID, userID, role,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return fmt.Errorf("user %s already a member of org %s", userID, orgID)
		}
		return fmt.Errorf("add org member: %w", err)
	}
	return nil
}

// SetTOTPSecret stores the TOTP secret for userID.
func (s *Store) SetTOTPSecret(ctx context.Context, userID uuid.UUID, secret string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE identity.users SET totp_secret = $1 WHERE id = $2`,
		secret, userID,
	)
	if err != nil {
		return fmt.Errorf("set totp secret: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("user %s not found", userID)
	}
	return nil
}
