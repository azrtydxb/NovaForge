package deployment

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/svcauth"
)

type cleanupIdentity struct {
	org, operation uuid.UUID
	attempt        int
}

// claimCleanup is a platform-worker exception returning only opaque identities.
// It never relies on the author still existing or holding an unexpired grant.
// Updating scheduling timestamps before I/O prevents an unavailable first target
// starving later obligations, including across restarts and multiple replicas.
func (s *Service) claimCleanup(ctx context.Context, limit int) ([]cleanupIdentity, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || !scope.IsPlatformWorker() || scope.PlatformWorker != "deployment-cleanup" {
		return nil, errors.New("deployment cleanup discovery requires named platform worker")
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("invalid cleanup batch limit")
	}
	rows, err := s.pool.Query(ctx, `WITH selected AS (
 SELECT org_id,operation_id,attempt FROM deployment.credential_obligations
 WHERE resolved_at IS NULL
 ORDER BY cleanup_attempted_at ASC NULLS FIRST,org_id,operation_id,attempt
 LIMIT $1 FOR UPDATE SKIP LOCKED)
 UPDATE deployment.credential_obligations c SET cleanup_attempted_at=clock_timestamp()
 FROM selected s WHERE c.org_id=s.org_id AND c.operation_id=s.operation_id AND c.attempt=s.attempt
 RETURNING c.org_id,c.operation_id,c.attempt`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cleanupIdentity
	for rows.Next() {
		var i cleanupIdentity
		if err := rows.Scan(&i.org, &i.operation, &i.attempt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// CleanupPending re-enters each discovered organization's scope through verified
// service credentials. It is not exposed as a public RPC or agent tool.
func (s *Service) CleanupPending(ctx context.Context, hmacSecret string, limit int) error {
	ids, err := s.claimCleanup(ctx, limit)
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		token, err := svcauth.Mint(hmacSecret, "deployment-cleanup", id.org, time.Minute)
		if err != nil {
			return err
		}
		scope, err := svcauth.ScopeFromToken(hmacSecret, token)
		if err != nil {
			return err
		}
		call, cancel := context.WithTimeout(authz.WithScope(ctx, scope), 20*time.Second)
		op, err := s.Get(call, id.operation)
		if err == nil {
			found := false
			for _, c := range op.Credentials {
				if c.Attempt == id.attempt {
					found = true
					err = s.CleanupCredentials(call, id.operation, id.attempt, c.ProviderBinding)
					break
				}
			}
			if !found {
				err = ErrConflict
			}
		}
		cancel()
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// RunCleanup blocks until shutdown; startup launches it in a goroutine. Missing
// configuration/evidence is retried, never interpreted as provider confirmation.
func (s *Service) RunCleanup(ctx context.Context, hmacSecret string) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		token, err := svcauth.MintPlatform(hmacSecret, "deployment-cleanup", time.Minute)
		if err == nil {
			name, verifyErr := svcauth.VerifyPlatform(hmacSecret, token)
			if verifyErr == nil {
				scope := authz.Scope{ActorKind: "service", PlatformWorker: name}
				err = s.CleanupPending(authz.WithScope(ctx, scope), hmacSecret, 25)
			} else {
				err = verifyErr
			}
		}
		if err != nil && ctx.Err() == nil {
			log.Print("deployment: credential cleanup pending; will retry")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
