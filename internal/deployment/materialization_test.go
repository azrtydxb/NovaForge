package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/secrets"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMaterializerConfigurationAcceptsOperatorCredentialName(t *testing.T) {
	s, _, _, req, _ := fixture(t)
	helm, _ := recoveryHelm(t, &partialCredentials{})
	target := s.targets[req.Target]
	raw, err := json.Marshal(map[string]any{"targets": []any{map[string]any{"name": target.Name, "org_id": target.OrgID, "repo_id": target.RepoID, "environment": target.Environment, "helm": helm.config, "credential_name": "RELEASE_KUBECONFIG"}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewConfiguredService(s.pool, s.approvals, path, Dependencies{ResolveRun: s.resolveRun, Kubernetes: helm.client, Credentials: &partialCredentials{}}); err != nil {
		t.Fatal(err)
	}
	built, err := NewConfiguredService(s.pool, s.approvals, path, Dependencies{ResolveRun: s.resolveRun, Kubernetes: helm.client, CredentialOwner: &materializationOwner{}, HMACSecret: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := built.targets[req.Target].Executor.(*HelmExecutor).credentials.(*SecretMaterializer); !ok {
		t.Fatal("factory did not compose owner adapter")
	}
}

type materializationOwner struct {
	prepareErr error
	requests   []secrets.DeploymentCredentialRequest
}

func (o *materializationOwner) PrepareDeployment(ctx context.Context, r secrets.DeploymentCredentialRequest) (secrets.DeploymentCredential, error) {
	scope, _ := authz.FromContext(ctx)
	if scope.ServiceName != "deployment-credentials" {
		return secrets.DeploymentCredential{}, errors.New("wrong owner identity")
	}
	o.requests = append(o.requests, r)
	return secrets.DeploymentCredential{Kubeconfig: "controlled-test-kubeconfig", ExpiresAt: r.ExpiresAt}, o.prepareErr
}
func (o *materializationOwner) RevokeDeployment(ctx context.Context, r secrets.DeploymentCredentialRequest) (secrets.CleanupStatus, error) {
	scope, _ := authz.FromContext(ctx)
	if scope.ServiceName != "deployment-cleanup" {
		return secrets.CleanupStatus{}, errors.New("wrong cleanup identity")
	}
	return secrets.CleanupStatus{Fenced: true}, nil
}
func materializerFixture(t *testing.T) (*SecretMaterializer, *Service, context.Context, Operation, *fake.Clientset, *materializationOwner) {
	t.Helper()
	s, ctx, _, req, _ := fixture(t)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	conn, unlock, err := s.lock(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.start(ctx, conn, op, "execute"); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	op, err = s.Get(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	client := fake.NewSimpleClientset()
	owner := &materializationOwner{}
	m, err := NewSecretMaterializer(s.pool, client, owner, uuid.NewString(), []CredentialBinding{{Target: *s.targets[req.Target], Namespace: "executor", Name: "RELEASE_KUBECONFIG"}})
	if err != nil {
		t.Fatal(err)
	}
	client.PrependReactor("delete", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
		action := a.(ktesting.DeleteAction)
		options := action.GetDeleteOptions()
		if options.Preconditions == nil || options.Preconditions.UID == nil {
			t.Error("missing UID delete precondition")
			return true, nil, errors.New("missing UID")
		}
		existing, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("secrets"), action.GetNamespace(), action.GetName())
		if err != nil {
			return true, nil, err
		}
		if existing.(*corev1.Secret).UID != *options.Preconditions.UID {
			return true, nil, apierrors.NewConflict(corev1.Resource("secrets"), action.GetName(), errors.New("UID changed"))
		}
		return false, nil, nil
	})
	return m, s, ctx, op, client, owner
}
func createMaterialization(client *fake.Clientset, a ktesting.Action) (*corev1.Secret, error) {
	object := a.(ktesting.CreateAction).GetObject().(*corev1.Secret).DeepCopy()
	object.UID = types.UID(uuid.NewString())
	err := client.Tracker().Create(corev1.SchemeGroupVersion.WithResource("secrets"), object, object.Namespace)
	return object, err
}
func TestMaterializerHeldCreateCancellationAndRestart(t *testing.T) {
	m, s, ctx, op, client, _ := materializerFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	client.PrependReactor("create", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
		close(started)
		<-release
		obj, err := createMaterialization(client, a)
		return true, obj, err
	})
	call, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := m.Prepare(call, op, "executor", 10*time.Minute); result <- err }()
	<-started
	cancel()
	// While Create is in flight, NotFound is not proof that no Secret can appear.
	// A separate empty client models absence while the held Create has not
	// committed; client-go fake serializes reactors. After completion we use the
	// original tracker to recover the actual late UID.
	cleanupClient := fake.NewSimpleClientset()
	restarted, err := NewSecretMaterializer(s.pool, cleanupClient, m.owner, m.secret, []CredentialBinding{m.bindings[op.Target]})
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Revoke(ctx, op, "executor"); !errors.Is(err, ErrUncertain) {
		t.Fatalf("late create incorrectly clean: %v", err)
	}
	close(release)
	if err = <-result; err == nil {
		t.Fatal("canceled/tombstoned prepare returned credential")
	}
	restarted.client = client
	if err = restarted.Revoke(ctx, op, "executor"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.CoreV1().Secrets("executor").Get(ctx, CredentialSecretName(op), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("late Secret remains", err)
	}
	if _, err = restarted.Prepare(ctx, op, "executor", 10*time.Minute); err == nil {
		t.Fatal("tombstone reopened")
	}
}
func TestMaterializerLostCreateReplyRecoversExactUID(t *testing.T) {
	m, s, ctx, op, client, _ := materializerFixture(t)
	client.PrependReactor("create", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
		_, err := createMaterialization(client, a)
		if err != nil {
			return true, nil, err
		}
		return true, nil, errors.New("lost create reply")
	})
	if _, err := m.Prepare(ctx, op, "executor", 10*time.Minute); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	restarted, err := NewSecretMaterializer(s.pool, client, m.owner, m.secret, []CredentialBinding{m.bindings[op.Target]})
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Revoke(ctx, op, "executor"); err != nil {
		t.Fatal(err)
	}
	var phase, uid string
	if err = s.pool.QueryRow(ctx, `SELECT phase,secret_uid FROM deployment.materializations WHERE org_id=$1 AND operation_id=$2`, op.OrgID, op.ID).Scan(&phase, &uid); err != nil {
		t.Fatal(err)
	}
	if phase != "absent" || uid == "" {
		t.Fatal("cleanup lacks durable UID evidence", phase, uid)
	}
}
func TestMaterializerScopeForeignSecretAndBeforePrepareFence(t *testing.T) {
	m, _, ctx, op, client, owner := materializerFixture(t)
	wrong := authz.WithScope(ctx, authz.Scope{OrgID: uuid.New(), ActorKind: "service", ServiceName: "deployment-cleanup"})
	if _, err := m.Prepare(wrong, op, "executor", 10*time.Minute); err == nil {
		t.Fatal("foreign org prepare")
	}
	if err := m.Revoke(wrong, op, "executor"); err == nil {
		t.Fatal("foreign org revoke")
	}
	bad := op
	bad.RepoID = uuid.New()
	if _, err := m.Prepare(ctx, bad, "executor", 10*time.Minute); err == nil {
		t.Fatal("foreign repo prepare")
	}
	if len(owner.requests) != 0 {
		t.Fatal("scope denial reached issuer")
	}
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: CredentialSecretName(op), Namespace: "executor", UID: "foreign"}}
	if err := client.Tracker().Create(corev1.SchemeGroupVersion.WithResource("secrets"), foreign, "executor"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Prepare(ctx, op, "executor", 10*time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign Secret accepted", err)
	}
	if err := m.Revoke(ctx, op, "executor"); err != nil {
		t.Fatal(err)
	}
	got, err := client.CoreV1().Secrets("executor").Get(ctx, foreign.Name, metav1.GetOptions{})
	if err != nil || got.UID != foreign.UID {
		t.Fatal("foreign Secret touched", err)
	}
	// A separate attempt can be fenced before it has even called the issuer.
	op.Attempts = append(op.Attempts, Attempt{Number: 2, StartedAt: time.Now(), CredentialExpiresAt: op.Attempts[0].CredentialExpiresAt})
	_, err = m.pool.Exec(ctx, `INSERT INTO deployment.attempts(org_id,operation_id,number,kind,actor_id,actor_kind,state,started_at,credential_expires_at) VALUES($1,$2,2,'execute',$3,'agent','running',$4,$5)`, op.OrgID, op.ID, op.ActorID, op.Attempts[1].StartedAt, op.Attempts[1].CredentialExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Revoke(ctx, op, "executor"); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Prepare(ctx, op, "executor", 10*time.Minute); err == nil {
		t.Fatal("pre-prepare tombstone bypass")
	}
}
func TestMaterializerPreservesHardExpiryFailure(t *testing.T) {
	m, s, ctx, op, client, owner := materializerFixture(t)
	owner.prepareErr = secrets.ErrHardExpiryUnavailable
	if _, err := m.Prepare(ctx, op, "executor", 10*time.Minute); !errors.Is(err, secrets.ErrHardExpiryUnavailable) {
		t.Fatal(err)
	}
	if len(client.Actions()) != 0 {
		t.Fatal("hard expiry failure still materialized")
	}
	if err := m.Revoke(ctx, op, "executor"); err != nil {
		t.Fatal(err)
	}
	var closed bool
	if err := s.pool.QueryRow(ctx, `SELECT closed FROM deployment.materializations WHERE org_id=$1 AND operation_id=$2`, op.OrgID, op.ID).Scan(&closed); err != nil || !closed {
		t.Fatal("missing tombstone", err)
	}
}

func TestMaterializerActualSecretsOwnerRefusesUnqualifiedEngine(t *testing.T) {
	m, s, ctx, op, client, _ := materializerFixture(t)
	if err := database.Migrate(s.pool.Config().ConnString(), "secrets", secrets.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	broker, err := secrets.NewOpenBaoBroker(s.pool, []byte("controlled-test-encryption-key"), secrets.OpenBaoConfig{Endpoint: "http://127.0.0.1:1", TokenFile: filepath.Join(t.TempDir(), "not-read"), Bindings: []secrets.OpenBaoBinding{{OrgID: op.OrgID, Environment: op.Environment, Name: "RELEASE_KUBECONFIG", Path: "kubernetes/creds/release", Method: "POST", Field: "kubeconfig"}}})
	if err != nil {
		t.Fatal(err)
	}
	m.owner = broker
	if _, err = m.Prepare(ctx, op, "executor", 10*time.Minute); !errors.Is(err, secrets.ErrHardExpiryUnavailable) {
		t.Fatalf("actual owner must fail closed: %v", err)
	}
	if len(client.Actions()) != 0 {
		t.Fatal("unqualified owner materialized Kubernetes Secret")
	}
	if err = m.Revoke(ctx, op, "executor"); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Prepare(ctx, op, "executor", 10*time.Minute); err == nil {
		t.Fatal("actual owner fence reopened")
	}
}

func TestMaterializerKnownUIDDoesNotDeleteReplacement(t *testing.T) {
	m, _, ctx, op, client, _ := materializerFixture(t)
	client.PrependReactor("create", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
		obj, err := createMaterialization(client, a)
		return true, obj, err
	})
	if _, err := m.Prepare(ctx, op, "executor", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	resource := corev1.SchemeGroupVersion.WithResource("secrets")
	if err := client.Tracker().Delete(resource, "executor", CredentialSecretName(op)); err != nil {
		t.Fatal(err)
	}
	replacement := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: CredentialSecretName(op), Namespace: "executor", UID: "replacement"}}
	if err := client.Tracker().Create(resource, replacement, "executor"); err != nil {
		t.Fatal(err)
	}
	if err := m.Revoke(ctx, op, "executor"); err != nil {
		t.Fatal(err)
	}
	got, err := client.CoreV1().Secrets("executor").Get(ctx, replacement.Name, metav1.GetOptions{})
	if err != nil || got.UID != "replacement" {
		t.Fatal("replacement deleted", err)
	}
}
