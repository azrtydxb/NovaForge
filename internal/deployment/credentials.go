package deployment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/novaforge/novaforge/internal/authz"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A journal never falls back to a pool: losing the destination lock fences all
// dispatch/cleanup acknowledgements, leaving the durable obligation outstanding.
type credentialJournal struct{ conn *pgx.Conn }

func (s *Service) lock(ctx context.Context, op Operation) (*pgx.Conn, func(), error) {
	select {
	case s.lockSlots <- struct{}{}:
	default:
		return nil, nil, ErrBusy
	}
	conn, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig.Copy())
	if err != nil {
		<-s.lockSlots
		return nil, nil, err
	}
	unlock := func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Close(cleanup)
		<-s.lockSlots
	}
	var acquired bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, "deployment/"+op.Destination).Scan(&acquired); err != nil {
		unlock()
		return nil, nil, err
	}
	if !acquired {
		unlock()
		return nil, nil, ErrBusy
	}
	return conn, unlock, nil
}
func (j *credentialJournal) pending(ctx context.Context, op Operation) (bool, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID != op.OrgID {
		return false, ErrConflict
	}
	var pending bool
	err = j.conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployment.credential_obligations WHERE org_id=$1 AND operation_id=$2 AND resolved_at IS NULL)`, scope.OrgID, op.ID).Scan(&pending)
	return pending, err
}
func (j *credentialJournal) update(ctx context.Context, op Operation, binding string, dispatch bool) error {
	if j == nil || len(op.Attempts) == 0 {
		return ErrUncertain
	}
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID != op.OrgID {
		return ErrConflict
	}
	number := op.Attempts[len(op.Attempts)-1].Number
	sql := `UPDATE deployment.credential_obligations SET resolved_at=now() WHERE org_id=$1 AND operation_id=$2 AND attempt=$3 AND provider_binding=$4 AND resolved_at IS NULL`
	if dispatch {
		sql = `UPDATE deployment.credential_obligations SET phase='dispatching' WHERE org_id=$1 AND operation_id=$2 AND attempt=$3 AND provider_binding=$4 AND resolved_at IS NULL`
	}
	tag, err := j.conn.Exec(ctx, sql, scope.OrgID, op.ID, number, binding)
	if err != nil || tag.RowsAffected() != 1 {
		return ErrUncertain
	}
	return nil
}
func (j *credentialJournal) require(ctx context.Context, op Operation, binding string) error {
	if j == nil || len(op.Attempts) == 0 {
		return ErrUncertain
	}
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID != op.OrgID {
		return ErrConflict
	}
	var found bool
	err = j.conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployment.credential_obligations WHERE org_id=$1 AND operation_id=$2 AND attempt=$3 AND provider_binding=$4 AND resolved_at IS NULL)`, scope.OrgID, op.ID, op.Attempts[len(op.Attempts)-1].Number, binding).Scan(&found)
	if err != nil || !found {
		return ErrUncertain
	}
	return nil
}

// CleanupCredentials is an internal janitor seam, not a human/tool/RPC "mark
// cleaned" operation. Only an authenticated org service may call it. The trusted
// configured provider must actually revoke/fence; caller assertions never clear
// the journal. It changes no delivery state and submits no workload/credential.
// Parent wiring must restrict this seam to its cleanup service identity.
func (s *Service) CleanupCredentials(ctx context.Context, id uuid.UUID, attempt int, providerBinding string) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil || scope.ActorKind != "service" {
		return errors.New("credential cleanup requires an organization service")
	}
	op, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	target := s.targets[op.Target]
	if target == nil || target.OrgID != scope.OrgID || target.RepoID != op.RepoID || target.Destination != op.Destination || target.Revision != op.TargetRevision {
		return ErrConflict
	}
	h, ok := target.Executor.(*HelmExecutor)
	if !ok || providerBinding != h.Revision() {
		return ErrConflict
	}
	conn, unlock, err := s.lock(ctx, op)
	if err != nil {
		return err
	}
	defer unlock()
	op, err = s.Get(ctx, id)
	if err != nil {
		return err
	}
	op.journal = &credentialJournal{conn: conn}
	for _, c := range op.Credentials {
		if c.Attempt != attempt {
			continue
		}
		if c.ProviderBinding != providerBinding {
			return ErrConflict
		}
		if c.ResolvedAt != nil {
			return nil
		}
		if err = h.cleanupCredential(ctx, op, c); err != nil {
			return fmt.Errorf("%w: credential cleanup not confirmed", ErrUncertain)
		}
		return nil
	}
	return ErrConflict
}

// revoke is always cancellation-safe and bounded. A successful provider response
// is acknowledged on the original lock session; failure leaves durable work.
func (h *HelmExecutor) revoke(ctx context.Context, op Operation) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := h.credentials.Revoke(cleanup, op, h.config.ExecutionNamespace); err != nil {
		return ErrUncertain
	}
	return op.journal.update(cleanup, op, h.Revision(), false)
}
func (h *HelmExecutor) cleanupCredential(ctx context.Context, op Operation, c CredentialObligation) error {
	if c.ProviderBinding != h.Revision() {
		return ErrConflict
	}
	index := -1
	for i, a := range op.Attempts {
		if a.Number == c.Attempt {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrConflict
	}
	op.Attempts = op.Attempts[:index+1]
	name := strings.TrimSuffix(CredentialSecretName(op), "-creds")
	job, err := h.client.BatchV1().Jobs(h.config.ExecutionNamespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && c.Phase == "preparing" {
		// No create can precede the durable dispatch marker. Revoke must fence a
		// delayed Prepare from a lost lock owner, not just delete an existing Secret.
		return h.revoke(ctx, op)
	}
	if err != nil || !h.matches(job, op, op.Attempts[index].Kind) {
		return ErrUncertain
	}
	// Existing executor evidence supersedes a preparation-only checkpoint (for
	// example, a pre-journal migration). Never acknowledge it as no-dispatch.
	if err = op.journal.update(ctx, op, h.Revision(), true); err != nil {
		return ErrUncertain
	}
	terminal := false
	for _, condition := range job.Status.Conditions {
		if condition.Status == corev1.ConditionTrue && (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) {
			terminal = true
		}
	}
	if !terminal {
		return ErrUncertain
	}
	if _, err = h.terminalPod(ctx, job, op); err != nil {
		return ErrUncertain
	}
	return h.revoke(ctx, op)
}
