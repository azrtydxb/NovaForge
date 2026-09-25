package deployment

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/secrets"
	"github.com/novaforge/novaforge/internal/svcauth"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// DeploymentCredentialOwner is the secrets owner's API, injected in the gates
// process. No deployment code reads issuer tables or stores underlying leases.
type DeploymentCredentialOwner interface {
	PrepareDeployment(context.Context, secrets.DeploymentCredentialRequest) (secrets.DeploymentCredential, error)
	RevokeDeployment(context.Context, secrets.DeploymentCredentialRequest) (secrets.CleanupStatus, error)
}

var _ DeploymentCredentialOwner = (*secrets.Broker)(nil)

type CredentialBinding struct {
	Target    Target
	Namespace string
	Name      string
}

type SecretMaterializer struct {
	pool     *pgxpool.Pool
	client   kubernetes.Interface
	owner    DeploymentCredentialOwner
	secret   string
	bindings map[string]CredentialBinding
}

func NewSecretMaterializer(pool *pgxpool.Pool, client kubernetes.Interface, owner DeploymentCredentialOwner, hmacSecret string, bindings []CredentialBinding) (*SecretMaterializer, error) {
	if pool == nil || client == nil || owner == nil || hmacSecret == "" {
		return nil, errors.New("deployment materialization requires database, Kubernetes, credential owner and service identity")
	}
	m := &SecretMaterializer{pool: pool, client: client, owner: owner, secret: hmacSecret, bindings: map[string]CredentialBinding{}}
	for _, b := range bindings {
		if b.Target.OrgID == uuid.Nil || b.Target.RepoID == uuid.Nil || b.Target.Name == "" || b.Target.Revision == "" || b.Target.Destination == "" || b.Namespace == "" || !secrets.ValidName(b.Name) {
			return nil, ErrConflict
		}
		if _, ok := m.bindings[b.Target.Name]; ok {
			return nil, ErrConflict
		}
		m.bindings[b.Target.Name] = b
	}
	return m, nil
}

type materialization struct {
	request         secrets.DeploymentCredentialRequest
	marker          uuid.UUID
	phase, uid      string
	closed          bool
	issuedExpiresAt *time.Time
}

// intent creates a durable fence even when cancellation arrives before Prepare.
// Deadline derives from the durable attempt, so restart/revoke cannot change the
// secrets owner's immutable request or extend an already-issued credential.
func (m *SecretMaterializer) intent(ctx context.Context, op Operation, namespace string, closeIntent bool) (materialization, error) {
	var row materialization
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil || scope.OrgID != op.OrgID || len(op.Attempts) == 0 {
		return row, ErrConflict
	}
	b, ok := m.bindings[op.Target]
	if !ok || b.Target.OrgID != scope.OrgID || b.Target.RepoID != op.RepoID || b.Target.Environment != op.Environment || b.Target.Revision != op.TargetRevision || b.Target.Destination != op.Destination || b.Namespace != namespace {
		return row, ErrConflict
	}
	a := op.Attempts[len(op.Attempts)-1]
	if a.Number < 1 || a.StartedAt.IsZero() || credentialDeadline(op).IsZero() {
		return row, ErrConflict
	}
	req := secrets.DeploymentCredentialRequest{OperationID: op.ID, AttemptID: uuid.NewSHA1(op.ID, []byte(strconv.Itoa(a.Number))), RepoID: op.RepoID, RunID: op.RunID, Target: op.Target, Environment: op.Environment, ProviderBinding: op.TargetRevision, Name: b.Name, ExpiresAt: credentialDeadline(op)}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return row, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO deployment.materializations(org_id,operation_id,attempt,intent,namespace,secret_name,marker,closed) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, scope.OrgID, op.ID, a.Number, req, namespace, CredentialSecretName(op), uuid.New(), closeIntent)
	if err != nil {
		return row, err
	}
	var matches bool
	err = tx.QueryRow(ctx, `SELECT intent=$4::jsonb AND namespace=$5 AND secret_name=$6,marker,phase,secret_uid,closed,issued_expires_at FROM deployment.materializations WHERE org_id=$1 AND operation_id=$2 AND attempt=$3 FOR UPDATE`, scope.OrgID, op.ID, a.Number, req, namespace, CredentialSecretName(op)).Scan(&matches, &row.marker, &row.phase, &row.uid, &row.closed, &row.issuedExpiresAt)
	if err != nil {
		return row, err
	}
	if !matches {
		return row, ErrConflict
	}
	if closeIntent {
		_, err = tx.Exec(ctx, `UPDATE deployment.materializations SET closed=true WHERE org_id=$1 AND operation_id=$2 AND attempt=$3`, scope.OrgID, op.ID, a.Number)
		if err != nil {
			return row, err
		}
		row.closed = true
	}
	row.request = req
	return row, tx.Commit(ctx)
}

func (m *SecretMaterializer) ownerContext(ctx context.Context, org uuid.UUID, cleanup bool) (context.Context, error) {
	name := "deployment-credentials"
	if cleanup {
		name = "deployment-cleanup"
	}
	token, err := svcauth.Mint(m.secret, name, org, time.Minute)
	if err != nil {
		return nil, err
	}
	scope, err := svcauth.ScopeFromToken(m.secret, token)
	if err != nil {
		return nil, err
	}
	return authz.WithScope(ctx, scope), nil
}

func (m *SecretMaterializer) Prepare(ctx context.Context, op Operation, namespace string, ttl time.Duration) (Credential, error) {
	if ttl != 10*time.Minute || (op.ActorKind == "agent" && op.AuthorizedUntil == nil) {
		return Credential{}, ErrConflict
	}
	row, err := m.intent(ctx, op, namespace, false)
	if err != nil {
		return Credential{}, err
	}
	if row.closed || row.phase == "absent" {
		return Credential{}, ErrUncertain
	}
	if !coversExecution(row.request.ExpiresAt) {
		return Credential{}, errors.New("deployment authority does not cover the execution window")
	}
	if row.phase != "intent" {
		return m.recoverPrepared(ctx, op, namespace, row)
	}
	ownerCtx, err := m.ownerContext(ctx, op.OrgID, false)
	if err != nil {
		return Credential{}, err
	}
	issued, err := m.owner.PrepareDeployment(ownerCtx, row.request)
	if err != nil {
		return Credential{}, err
	} // Production currently returns ErrHardExpiryUnavailable.
	if issued.Kubeconfig == "" || !issued.ExpiresAt.After(time.Now()) || issued.ExpiresAt.After(row.request.ExpiresAt) {
		return Credential{}, ErrUncertain
	}
	if !coversExecution(issued.ExpiresAt) {
		return Credential{}, errors.New("deployment credential does not cover the execution window")
	}
	// Persist the owner-returned horizon with the create boundary, so restart
	// never reports the longer requested lifetime for a shorter credential.
	// Persist the possible-create boundary before touching Kubernetes. Tombstoning
	// races this CAS, not a process-local mutex; only one caller may ever create.
	tag, err := m.pool.Exec(ctx, `UPDATE deployment.materializations SET phase='creating',issued_expires_at=$4 WHERE org_id=$1 AND operation_id=$2 AND attempt=$3 AND NOT closed AND phase='intent'`, op.OrgID, op.ID, op.Attempts[len(op.Attempts)-1].Number, issued.ExpiresAt.UTC().Truncate(time.Microsecond))
	if err != nil || tag.RowsAffected() != 1 {
		return Credential{}, ErrUncertain
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: CredentialSecretName(op), Namespace: namespace, Annotations: map[string]string{"novaforge.dev/materialization": row.marker.String()}}, Immutable: boolPointer(true), Data: map[string][]byte{"config": []byte(issued.Kubeconfig)}, Type: corev1.SecretTypeOpaque}
	created, createErr := m.client.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	// A definitive AlreadyExists response means this one create did not happen.
	// Never adopt/delete a preexisting Secret, even if its name matches our intent.
	if apierrors.IsAlreadyExists(createErr) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = m.pool.Exec(cleanup, `UPDATE deployment.materializations SET phase='absent' WHERE org_id=$1 AND operation_id=$2 AND attempt=$3 AND phase='creating' AND secret_uid=''`, op.OrgID, op.ID, op.Attempts[len(op.Attempts)-1].Number)
		return Credential{}, ErrConflict
	}
	if createErr != nil {
		return Credential{}, ErrUncertain
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err = m.recordUID(cleanup, op, row, created); err != nil {
		return Credential{}, err
	}
	return m.recoverPrepared(ctx, op, namespace, row)
}
func boolPointer(v bool) *bool { return &v }

func (m *SecretMaterializer) recordUID(ctx context.Context, op Operation, row materialization, secret *corev1.Secret) error {
	if secret == nil || secret.UID == "" || secret.Name != CredentialSecretName(op) || secret.Annotations["novaforge.dev/materialization"] != row.marker.String() {
		return ErrUncertain
	}
	tag, err := m.pool.Exec(ctx, `UPDATE deployment.materializations SET phase='materialized',secret_uid=$4 WHERE org_id=$1 AND operation_id=$2 AND attempt=$3 AND (phase='creating' OR (phase='materialized' AND secret_uid=$4))`, op.OrgID, op.ID, op.Attempts[len(op.Attempts)-1].Number, string(secret.UID))
	if err != nil || tag.RowsAffected() != 1 {
		return ErrUncertain
	}
	return nil
}
func (m *SecretMaterializer) recoverPrepared(ctx context.Context, op Operation, namespace string, row materialization) (Credential, error) {
	secret, err := m.client.CoreV1().Secrets(namespace).Get(ctx, CredentialSecretName(op), metav1.GetOptions{})
	if err != nil {
		return Credential{}, ErrUncertain
	}
	if row.uid != "" && row.uid != string(secret.UID) {
		return Credential{}, ErrUncertain
	}
	if err = m.recordUID(ctx, op, row, secret); err != nil {
		return Credential{}, err
	}
	fresh, err := m.intent(ctx, op, namespace, false)
	if err != nil || fresh.closed || fresh.phase != "materialized" || fresh.issuedExpiresAt == nil || !coversExecution(*fresh.issuedExpiresAt) || fresh.issuedExpiresAt.After(fresh.request.ExpiresAt) {
		return Credential{}, ErrUncertain
	}
	return Credential{SecretName: secret.Name, ExpiresAt: *fresh.issuedExpiresAt}, nil
}

func (m *SecretMaterializer) Revoke(ctx context.Context, op Operation, namespace string) error {
	row, err := m.intent(ctx, op, namespace, true)
	if err != nil {
		return err
	}
	ownerCtx, err := m.ownerContext(ctx, op.OrgID, true)
	if err != nil {
		return err
	}
	status, issuerErr := m.owner.RevokeDeployment(ownerCtx, row.request)
	if issuerErr == nil && (!status.Fenced || status.Pending != 0) {
		issuerErr = ErrUncertain
	}
	// Issuer fencing and Kubernetes fencing are independent obligations. Attempt
	// both even when one authority is unavailable; neither substitutes for the other.
	return errors.Join(issuerErr, m.removeSecret(ctx, op, namespace, row))
}
func (m *SecretMaterializer) removeSecret(ctx context.Context, op Operation, namespace string, row materialization) error {
	if row.phase == "intent" || row.phase == "absent" {
		return nil
	} // closed prevents any future create CAS
	secret, err := m.client.CoreV1().Secrets(namespace).Get(ctx, CredentialSecretName(op), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if row.uid == "" {
			return ErrUncertain
		} // late/unknown Create can still commit
		return m.markAbsent(ctx, op, row.uid)
	}
	if err != nil {
		return ErrUncertain
	}
	if row.uid != "" && row.uid != string(secret.UID) {
		return m.markAbsent(ctx, op, row.uid)
	} // original UID is gone; never delete replacement
	if err = m.recordUID(ctx, op, row, secret); err != nil {
		return err
	}
	uid := types.UID(secret.UID)
	if err = m.client.CoreV1().Secrets(namespace).Delete(ctx, secret.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
		return ErrUncertain
	}
	observed, err := m.client.CoreV1().Secrets(namespace).Get(ctx, secret.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) || (err == nil && observed.UID != uid) {
		return m.markAbsent(ctx, op, string(uid))
	}
	return ErrUncertain
}
func (m *SecretMaterializer) markAbsent(ctx context.Context, op Operation, uid string) error {
	tag, err := m.pool.Exec(ctx, `UPDATE deployment.materializations SET phase='absent' WHERE org_id=$1 AND operation_id=$2 AND attempt=$3 AND closed AND secret_uid=$4`, op.OrgID, op.ID, op.Attempts[len(op.Attempts)-1].Number, uid)
	if err != nil || tag.RowsAffected() != 1 {
		return ErrUncertain
	}
	return nil
}
