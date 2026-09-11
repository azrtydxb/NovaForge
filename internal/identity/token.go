package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// tokenBytes is the amount of entropy in a personal access token before
// encoding, not counting the "nf_" prefix.
const tokenBytes = 32

// Token is a row in the identity.access_tokens table.
type Token struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Name      string
	Scopes    []string
	ExpiresAt *time.Time
	RevokedAt *time.Time
}

// TokenStore manages personal access tokens. Only the SHA-256 hash of a
// token's plaintext is stored; the plaintext is returned exactly once, at
// creation time.
type TokenStore struct {
	pool *pgxpool.Pool
}

// NewTokenStore wraps pool as an identity.TokenStore.
func NewTokenStore(pool *pgxpool.Pool) *TokenStore {
	return &TokenStore{pool: pool}
}

func hashToken(plaintext string) []byte {
	sum := sha256.Sum256([]byte(plaintext))
	return sum[:]
}

// Create mints a new personal access token for userID and returns its
// plaintext (rendered as "nf_<base64url>") alongside the stored record.
func (s *TokenStore) Create(ctx context.Context, userID uuid.UUID, name string, scopes []string, expiresAt *time.Time) (string, Token, error) {
	// A token with no scopes is legitimate — it simply carries none — but the
	// column is NOT NULL, so nil must become an empty list rather than a
	// constraint violation at the far end of an API call.
	if scopes == nil {
		scopes = []string{}
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", Token{}, fmt.Errorf("generate token: %w", err)
	}
	plaintext := "nf_" + base64.RawURLEncoding.EncodeToString(raw)

	t := Token{
		ID:        uuid.New(),
		UserID:    userID,
		Name:      name,
		Scopes:    scopes,
		ExpiresAt: expiresAt,
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO identity.access_tokens (id, user_id, name, token_hash, scopes, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		t.ID, t.UserID, t.Name, hashToken(plaintext), t.Scopes, t.ExpiresAt,
	)
	if err != nil {
		return "", Token{}, fmt.Errorf("create token: %w", err)
	}
	return plaintext, t, nil
}

// Resolve looks up the token matching plaintext, rejecting revoked or
// expired tokens.
func (s *TokenStore) Resolve(ctx context.Context, plaintext string) (Token, error) {
	var t Token
	var expiresAt, revokedAt *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, name, scopes, expires_at, revoked_at
		 FROM identity.access_tokens WHERE token_hash = $1`,
		hashToken(plaintext),
	).Scan(&t.ID, &t.UserID, &t.Name, &t.Scopes, &expiresAt, &revokedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Token{}, errors.New("token not found")
		}
		return Token{}, fmt.Errorf("resolve token: %w", err)
	}
	t.ExpiresAt = expiresAt
	t.RevokedAt = revokedAt

	if revokedAt != nil {
		return Token{}, errors.New("token revoked")
	}
	if expiresAt != nil && expiresAt.Before(time.Now()) {
		return Token{}, errors.New("token expired")
	}
	return t, nil
}

// Revoke marks the token identified by id as revoked.
func (s *TokenStore) Revoke(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE identity.access_tokens SET revoked_at = now() WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("token %s not found", id)
	}
	return nil
}
