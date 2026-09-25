package secrets

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
)

// PendingRevocationOrganizations is narrowly platform-scoped discovery. It
// returns IDs only, including deleted orgs that Identity can no longer list.
// Every actual retry re-enters the org; unknown issuance and rotation-required
// rows remain pending, not acknowledged by a successful discovery pass.
func (b *Broker) PendingRevocationOrganizations(ctx context.Context) ([]uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || !scope.IsPlatformWorker() || scope.PlatformWorker != "gates-credentials" {
		return nil, errors.New("credential discovery requires platform worker")
	}
	rows, err := b.pool.Query(ctx, `SELECT org_id FROM secrets.secret_leases WHERE revocation_requested AND (revoked_at IS NULL OR (provider_endpoint='' AND redeemed_count>0)) GROUP BY org_id ORDER BY min(revocation_attempted_at) NULLS FIRST,org_id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RunRevocationWorker is started by the owning gates constructor, not by a
// request or the presence of new CI jobs. Provider calls have individual bounds.
func (b *Broker) RunRevocationWorker(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		worker := authz.WithScope(ctx, authz.Scope{ActorKind: "service", PlatformWorker: "gates-credentials"})
		ids, err := b.PendingRevocationOrganizations(worker)
		if err != nil && ctx.Err() == nil {
			slog.Warn("credential retry discovery unavailable")
		}
		for _, org := range ids {
			if ctx.Err() != nil {
				return
			}
			scoped, cancel := context.WithTimeout(authz.WithScope(context.WithoutCancel(ctx), authz.Scope{OrgID: org, ActorKind: "service", ServiceName: "gates-credentials"}), 15*time.Second)
			err := b.RetryRevocations(scoped)
			cancel()
			if err != nil {
				slog.Warn("credential revocation remains pending", "org", org)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
