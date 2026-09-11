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
	"github.com/redis/go-redis/v9"
)

// sessionTokenBytes is the amount of entropy in a session token before
// base64url encoding.
const sessionTokenBytes = 32

// SessionStore holds login sessions in Redis. Only the SHA-256 hash of a
// token is ever stored; the plaintext token exists solely in the client's
// possession.
type SessionStore struct {
	client *redis.Client
}

// NewSessionStore wraps client as an identity.SessionStore.
func NewSessionStore(client *redis.Client) *SessionStore {
	return &SessionStore{client: client}
}

func sessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "session:" + base64.RawURLEncoding.EncodeToString(sum[:])
}

// Create starts a new session for userID, valid for ttl, and returns the
// plaintext token the caller must present to Resolve.
func (s *SessionStore) Create(ctx context.Context, userID uuid.UUID, ttl time.Duration) (string, error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	if err := s.client.Set(ctx, sessionKey(token), userID.String(), ttl).Err(); err != nil {
		return "", fmt.Errorf("store session: %w", err)
	}
	return token, nil
}

// Resolve returns the user id for token, or an error if the session does not
// exist or has expired.
func (s *SessionStore) Resolve(ctx context.Context, token string) (uuid.UUID, error) {
	val, err := s.client.Get(ctx, sessionKey(token)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return uuid.UUID{}, errors.New("session not found or expired")
		}
		return uuid.UUID{}, fmt.Errorf("resolve session: %w", err)
	}
	userID, err := uuid.Parse(val)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("parse session user id: %w", err)
	}
	return userID, nil
}

// pendingTOTPTTL bounds how long a TOTP enrolment may stay half-finished.
const pendingTOTPTTL = 10 * time.Minute

// StashPendingTOTP holds a not-yet-confirmed TOTP secret for a user.
//
// The secret cannot go straight onto the user row: login treats a present
// secret as "two-factor enabled", so writing it before the user has proved they
// can generate a code would lock them out of their own account.
func (s *SessionStore) StashPendingTOTP(ctx context.Context, userID uuid.UUID, secret string) error {
	return s.client.Set(ctx, "totp:pending:"+userID.String(), secret, pendingTOTPTTL).Err()
}

// PendingTOTP returns the stashed secret, if the enrolment is still live.
func (s *SessionStore) PendingTOTP(ctx context.Context, userID uuid.UUID) (string, error) {
	v, err := s.client.Get(ctx, "totp:pending:"+userID.String()).Result()
	if errors.Is(err, redis.Nil) {
		return "", fmt.Errorf("no pending two-factor enrolment; start again with setup")
	}
	return v, err
}

// ClearPendingTOTP drops a completed or abandoned enrolment.
func (s *SessionStore) ClearPendingTOTP(ctx context.Context, userID uuid.UUID) error {
	return s.client.Del(ctx, "totp:pending:"+userID.String()).Err()
}
