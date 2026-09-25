package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
)

// DeploymentCredentialRequest is immutable operator-bound execution intent,
// not credential material. OperationID namespaces the broker run fence and
// AttemptID identifies exactly one execution/observation credential batch.
type DeploymentCredentialRequest struct {
	OperationID, AttemptID, RepoID, RunID      uuid.UUID
	Target, Environment, ProviderBinding, Name string
	ExpiresAt                                  time.Time
}

type DeploymentCredential struct {
	Kubeconfig string
	ExpiresAt  time.Time
}

// PrepareDeployment deliberately refuses all currently supported engines:
// an OpenBao lease timestamp is not proof of target-enforced hard expiry.
// Recording intent still makes cancellation/retry identity durable and exact.
// Qualification of a real target/engine must precede returning any kubeconfig.
func (b *Broker) PrepareDeployment(ctx context.Context, req DeploymentCredentialRequest) (DeploymentCredential, error) {
	if err := b.deploymentIntent(ctx, req, false); err != nil {
		return DeploymentCredential{}, err
	}
	if !req.ExpiresAt.After(time.Now()) {
		return DeploymentCredential{}, errors.New("deployment credential deadline expired")
	}
	if b.provider == nil {
		return DeploymentCredential{}, ErrProviderNotConfigured
	}
	scope, _ := authz.FromContext(ctx)
	if _, ok := b.provider.bindings[bindingKey{scope.OrgID, req.Environment, req.Name}]; !ok {
		return DeploymentCredential{}, ErrProviderNotConfigured
	}
	return DeploymentCredential{}, ErrHardExpiryUnavailable
}

// RevokeDeployment establishes the exact attempt fence even before Prepare.
// Unknown issuer outcomes/rotation remain pending; absence of a local Secret
// or expiration of a lease is never an acknowledgment of provider cleanup.
func (b *Broker) RevokeDeployment(ctx context.Context, req DeploymentCredentialRequest) (CleanupStatus, error) {
	if err := b.deploymentIntent(ctx, req, true); err != nil {
		return CleanupStatus{}, err
	}
	return b.RevokeRunLeases(ctx, req.OperationID, req.AttemptID)
}

func (b *Broker) deploymentIntent(ctx context.Context, req DeploymentCredentialRequest, closeIntent bool) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil || scope.ActorKind != "service" || (scope.ServiceName != "deployment-credentials" && !(closeIntent && scope.ServiceName == "deployment-cleanup")) {
		return errors.New("deployment credential service authority required")
	}
	if req.OperationID == uuid.Nil || req.AttemptID == uuid.Nil || req.RepoID == uuid.Nil || req.RunID == uuid.Nil || req.Target == "" || req.ProviderBinding == "" || !ValidName(req.Name) || req.ExpiresAt.IsZero() || (req.Environment != EnvironmentStaging && req.Environment != EnvironmentProduction) {
		return errors.New("invalid deployment credential intent")
	}
	req.ExpiresAt = req.ExpiresAt.UTC()
	encoded, err := json.Marshal(req)
	if err != nil {
		return err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	closed, err := lockScope(ctx, tx, scope.OrgID, req.OperationID, req.AttemptID)
	if err != nil {
		return err
	}
	if closed && !closeIntent {
		return ErrScopeClosed
	}
	if _, err := tx.Exec(ctx, `INSERT INTO secrets.deployment_credential_intents(org_id,operation_id,attempt_id,intent,closed) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, scope.OrgID, req.OperationID, req.AttemptID, encoded, closeIntent); err != nil {
		return err
	}
	var matches, previouslyClosed bool
	if err := tx.QueryRow(ctx, `SELECT intent=$4::jsonb,closed FROM secrets.deployment_credential_intents WHERE org_id=$1 AND operation_id=$2 AND attempt_id=$3 FOR UPDATE`, scope.OrgID, req.OperationID, req.AttemptID, encoded).Scan(&matches, &previouslyClosed); err != nil {
		return err
	}
	if !matches {
		return errors.New("deployment credential intent mismatch")
	}
	if previouslyClosed && !closeIntent {
		return ErrScopeClosed
	}
	if closeIntent {
		if _, err := tx.Exec(ctx, `UPDATE secrets.deployment_credential_intents SET closed=true WHERE org_id=$1 AND operation_id=$2 AND attempt_id=$3`, scope.OrgID, req.OperationID, req.AttemptID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE secrets.credential_attempts SET closed=true WHERE org_id=$1 AND run_id=$2 AND attempt_id=$3`, scope.OrgID, req.OperationID, req.AttemptID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
