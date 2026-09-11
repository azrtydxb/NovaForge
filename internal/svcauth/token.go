// Package svcauth mints and verifies the short-lived tokens platform services
// present to one another.
//
// Background workers have no human behind them: the CI scheduler reacts to a
// push event and must read the repository's workflow file, but it holds no
// user's credential. Rather than let such a caller through unauthenticated —
// which would mean any unauthenticated caller gets in — it presents a token
// signed with the shared HMAC secret, naming itself and the single organization
// it is acting for. The org comes from the event being handled, so a worker
// cannot reach beyond the organization whose work it is doing.
package svcauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Prefix marks a service token so it is never confused with a user credential.
const Prefix = "nfsvc."

// DefaultTTL bounds a service token's life. It is short because a worker mints
// one per unit of work, not per process.
const DefaultTTL = 5 * time.Minute

type claims struct {
	Service   string    `json:"svc"`
	OrgID     uuid.UUID `json:"org"`
	ExpiresAt int64     `json:"exp"`
}

// Mint returns a token for service acting within orgID.
func Mint(secret, service string, orgID uuid.UUID, ttl time.Duration) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("no HMAC secret configured for service authentication")
	}
	// Only an unset TTL takes the default. A negative one is a caller error and
	// must not be silently turned into a valid five-minute token.
	if ttl == 0 {
		ttl = DefaultTTL
	}
	body, err := json.Marshal(claims{
		Service: service, OrgID: orgID, ExpiresAt: time.Now().Add(ttl).Unix(),
	})
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	return Prefix + payload + "." + sign(secret, payload), nil
}

// Verify checks a token's signature and expiry, returning the service name and
// the organization it may act within.
func Verify(secret, token string) (string, uuid.UUID, error) {
	if secret == "" {
		return "", uuid.Nil, fmt.Errorf("no HMAC secret configured for service authentication")
	}
	rest, ok := strings.CutPrefix(token, Prefix)
	if !ok {
		return "", uuid.Nil, fmt.Errorf("not a service token")
	}
	payload, sig, ok := strings.Cut(rest, ".")
	if !ok {
		return "", uuid.Nil, fmt.Errorf("malformed service token")
	}
	// Constant-time comparison: a timing-variable check on a signature leaks
	// the signature a byte at a time.
	if !hmac.Equal([]byte(sig), []byte(sign(secret, payload))) {
		return "", uuid.Nil, fmt.Errorf("bad service token signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", uuid.Nil, fmt.Errorf("malformed service token payload: %w", err)
	}
	var c claims
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", uuid.Nil, fmt.Errorf("malformed service token claims: %w", err)
	}
	if time.Now().Unix() > c.ExpiresAt {
		return "", uuid.Nil, fmt.Errorf("service token expired")
	}
	if c.OrgID == uuid.Nil {
		return "", uuid.Nil, fmt.Errorf("service token carries no organization")
	}
	return c.Service, c.OrgID, nil
}

func sign(secret, payload string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
