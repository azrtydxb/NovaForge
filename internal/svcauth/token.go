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
	// Platform marks a token that names no organization (see MintPlatform).
	Platform bool `json:"plat,omitempty"`
}

// MintPlatform returns a token for a platform worker that acts across
// organizations, naming none. Verify refuses it — every org-scoped path uses
// Verify — so the only thing it can reach is an RPC that checks for a
// platform worker explicitly, and such an RPC returns ids only; the worker
// then mints an org-scoped token to act inside each organization.
func MintPlatform(secret, service string, ttl time.Duration) (string, error) {
	return mint(secret, claims{Service: service, Platform: true}, ttl)
}

// VerifyPlatform checks a platform token and returns the worker's name. An
// org-scoped token is refused.
func VerifyPlatform(secret, token string) (string, error) {
	c, err := verifyClaims(secret, token)
	if err != nil {
		return "", err
	}
	if !c.Platform || c.OrgID != uuid.Nil {
		return "", fmt.Errorf("not a platform token")
	}
	if c.Service == "" {
		return "", fmt.Errorf("platform token names no service")
	}
	return c.Service, nil
}

// Mint returns a token for service acting within orgID.
func Mint(secret, service string, orgID uuid.UUID, ttl time.Duration) (string, error) {
	return mint(secret, claims{Service: service, OrgID: orgID}, ttl)
}

// MintAgentRun returns the credential an agent run presents: agentID acting
// within orgID. The agent is named because capability grants are held by an
// agent — a credential that names only the organization gives a transport
// nobody to look a grant up for, so the grant could never be applied.
func MintAgentRun(secret string, orgID, agentID uuid.UUID, ttl time.Duration) (string, error) {
	if agentID == uuid.Nil {
		return "", fmt.Errorf("an agent run credential must name its agent")
	}
	return mint(secret, claims{Service: AgentRunService, OrgID: orgID, Actor: agentID}, ttl)
}

// ScopeFromToken verifies an org-scoped service token and classifies its
// holder into the scope every service applies: an agent run is the agent it
// names, anything else is the platform acting for one organization. It is the
// one place this is decided, so git-platform and the gRPC services cannot
// disagree about who an agent is. A platform token is refused here.
func ScopeFromToken(secret, token string) (authz.Scope, error) {
	c, err := verifyClaims(secret, token)
	if err != nil {
		return authz.Scope{}, err
	}
	if c.OrgID == uuid.Nil || c.Platform {
		return authz.Scope{}, fmt.Errorf("service token carries no organization")
	}
	scope := authz.Scope{OrgID: c.OrgID, ActorKind: actorKindFor(c.Service)}
	if scope.ActorKind == "agent" {
		scope.ActorID = c.Actor
	}
	return scope, nil
}

func mint(secret string, c claims, ttl time.Duration) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("no HMAC secret configured for service authentication")
	}
	// Only an unset TTL takes the default. A negative one is a caller error and
	// must not be silently turned into a valid five-minute token.
	if ttl == 0 {
		ttl = DefaultTTL
	}
	c.ExpiresAt = time.Now().Add(ttl).Unix()
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	return Prefix + payload + "." + sign(secret, payload), nil
}

// Verify checks a token's signature and expiry, returning the service name and
// the organization it may act within. A platform token, which names no
// organization, is refused.
func Verify(secret, token string) (string, uuid.UUID, error) {
	c, err := verifyClaims(secret, token)
	if err != nil {
		return "", uuid.Nil, err
	}
	if c.OrgID == uuid.Nil || c.Platform {
		return "", uuid.Nil, fmt.Errorf("service token carries no organization")
	}
	return c.Service, c.OrgID, nil
}

func verifyClaims(secret, token string) (claims, error) {
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
	return c, nil
}

func sign(secret, payload string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
