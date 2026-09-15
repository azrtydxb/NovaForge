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

	"github.com/novaforge/novaforge/internal/authz"
)

// Prefix marks a service token so it is never confused with a user credential.
const Prefix = "nfsvc."

// DefaultTTL bounds a service token's life. It is short because a worker mints
// one per unit of work, not per process.
const DefaultTTL = 5 * time.Minute

type claims struct {
	Service string    `json:"svc"`
	OrgID   uuid.UUID `json:"org"`
	// Actor is the agent an agent-run token acts as. It is omitted for the
	// platform's own workers, which act as nobody in particular.
	Actor     uuid.UUID `json:"act,omitempty"`
	ExpiresAt int64     `json:"exp"`
}

// Mint returns a token for service acting within orgID.
func Mint(secret, service string, orgID uuid.UUID, ttl time.Duration) (string, error) {
	return mint(secret, service, orgID, uuid.Nil, ttl)
}

// MintAgentRun returns the credential an agent run presents: agentID acting
// within orgID. The agent is named because capability grants are held by an
// agent — a credential that names only the organization gives a transport
// nobody to look a grant up for, so the grant could never be applied.
func MintAgentRun(secret string, orgID, agentID uuid.UUID, ttl time.Duration) (string, error) {
	if agentID == uuid.Nil {
		return "", fmt.Errorf("an agent run credential must name its agent")
	}
	return mint(secret, AgentRunService, orgID, agentID, ttl)
}

// ScopeFromToken verifies a service token and classifies its holder into the
// scope every service applies: an agent run is the agent it names, anything
// else is the platform acting for one organization. It is the one place this
// is decided, so git-platform and the gRPC services cannot disagree about who
// an agent is.
func ScopeFromToken(secret, token string) (authz.Scope, error) {
	c, err := verify(secret, token)
	if err != nil {
		return authz.Scope{}, err
	}
	scope := authz.Scope{OrgID: c.OrgID, ActorKind: actorKindFor(c.Service)}
	if scope.ActorKind == "agent" {
		scope.ActorID = c.Actor
	}
	return scope, nil
}

func mint(secret, service string, orgID, actor uuid.UUID, ttl time.Duration) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("no HMAC secret configured for service authentication")
	}
	// Only an unset TTL takes the default. A negative one is a caller error and
	// must not be silently turned into a valid five-minute token.
	if ttl == 0 {
		ttl = DefaultTTL
	}
	body, err := json.Marshal(claims{
		Service: service, OrgID: orgID, Actor: actor, ExpiresAt: time.Now().Add(ttl).Unix(),
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
	c, err := verify(secret, token)
	if err != nil {
		return "", uuid.Nil, err
	}
	return c.Service, c.OrgID, nil
}

func verify(secret, token string) (claims, error) {
	if secret == "" {
		return claims{}, fmt.Errorf("no HMAC secret configured for service authentication")
	}
	rest, ok := strings.CutPrefix(token, Prefix)
	if !ok {
		return claims{}, fmt.Errorf("not a service token")
	}
	payload, sig, ok := strings.Cut(rest, ".")
	if !ok {
		return claims{}, fmt.Errorf("malformed service token")
	}
	// Constant-time comparison: a timing-variable check on a signature leaks
	// the signature a byte at a time.
	if !hmac.Equal([]byte(sig), []byte(sign(secret, payload))) {
		return claims{}, fmt.Errorf("bad service token signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return claims{}, fmt.Errorf("malformed service token payload: %w", err)
	}
	var c claims
	if err := json.Unmarshal(raw, &c); err != nil {
		return claims{}, fmt.Errorf("malformed service token claims: %w", err)
	}
	if time.Now().Unix() > c.ExpiresAt {
		return claims{}, fmt.Errorf("service token expired")
	}
	if c.OrgID == uuid.Nil {
		return claims{}, fmt.Errorf("service token carries no organization")
	}
	return c, nil
}

func sign(secret, payload string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
