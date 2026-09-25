package gates_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/gates"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

func TestNamespacedSandboxStartupProfile(t *testing.T) {
	j, pool, target := journalFor(t, 1)
	c := namespacedClient(t)
	for name, mutate := range map[string]func(*gates.NamespacedSandboxConfig){
		"empty-image":   func(c *gates.NamespacedSandboxConfig) { c.Image = "" },
		"mutable-image": func(c *gates.NamespacedSandboxConfig) { c.Image = "analysis:latest" },
		"empty-app":     func(c *gates.NamespacedSandboxConfig) { c.ApplicationNamespace = "" },
		"invalid-app":   func(c *gates.NamespacedSandboxConfig) { c.ApplicationNamespace = "BAD_APP" },
		"app-collision": func(c *gates.NamespacedSandboxConfig) { c.ApplicationNamespace = "gate-sandbox" },
		"default-sa":    func(c *gates.NamespacedSandboxConfig) { c.ServiceAccount = "default" },
		"empty-sa":      func(c *gates.NamespacedSandboxConfig) { c.ServiceAccount = "" },
		"invalid-sa":    func(c *gates.NamespacedSandboxConfig) { c.ServiceAccount = "BAD_SA" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := namespacedConfig()
			mutate(&cfg)
			if _, err := gates.NewNamespacedSandbox(j, &rest.Config{Host: "http://unused.invalid"}, cfg); err == nil {
				t.Fatal("unsafe allocator config")
			}
			if _, err := gates.NewNamespacedSandboxObserver(j, c.CoreV1().Pods("gate-sandbox"), cfg); err == nil {
				t.Fatal("unsafe observer config")
			}
		})
	}
	for _, ns := range []string{"default", "kube-system", "kube-public", "kube-node-lease", "INVALID"} {
		if _, err := gates.NewSandboxJournal(context.Background(), pool, uuid.NewString(), ns, 1); err == nil {
			t.Fatalf("unsafe namespace %q", ns)
		}
	}
	s, err := newNamespacedFor(t, j, c)
	sandboxOK(t, err)
	intent := sandboxIntent()
	intent.ImageDigest = "sha256:" + strings.Repeat("f", 64)
	_, _, err = s.Allocate(scopedCtx(uuid.New()), intent)
	sandboxError(t, err, gates.ErrSandboxConflict)
	if len(c.Actions()) != 0 {
		t.Fatal("invalid profile caused Kubernetes action")
	}
	sandboxCount(t, pool, target, 0)
}

func TestNamespacedSandboxEveryIdentityField(t *testing.T) {
	j, pool, target := journalFor(t, 1)
	ctx := scopedCtx(uuid.New())
	c := namespacedClient(t)
	s, err := newNamespacedFor(t, j, c)
	sandboxOK(t, err)
	id, uid, err := s.Allocate(ctx, sandboxIntent())
	sandboxOK(t, err)
	_, err = j.ClaimExec(ctx, id, uid)
	sandboxOK(t, err)
	original, err := c.CoreV1().Pods(id.Namespace).Get(ctx, id.PodName, metav1.GetOptions{})
	sandboxOK(t, err)
	if errs := apivalidation.ValidateObjectMeta(&original.ObjectMeta, true, apivalidation.NameIsDNSSubdomain, field.NewPath("metadata")); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, fieldName := range []string{"InvocationID", "AttemptID", "RunID", "RepoID", "Gate", "Tool", "SourceSHA", "PolicySHA", "SnapshotDigest", "ImageDigest", "CommandDigest", "Container", "Containers", "OrgID", "Target", "Namespace", "PodName"} {
		t.Run(fieldName, func(t *testing.T) {
			changed := id
			f := reflect.ValueOf(&changed).Elem().FieldByName(fieldName)
			switch f.Kind() {
			case reflect.String:
				f.SetString(f.String() + "-different")
			case reflect.Array:
				f.Set(reflect.ValueOf(uuid.New()))
			case reflect.Slice:
				f.Set(reflect.ValueOf([]string{"injected"}))
			default:
				t.Fatal("unhandled identity field")
			}
			data, err := json.Marshal(changed)
			sandboxOK(t, err)
			p := original.DeepCopy()
			p.Annotations["novaforge.io/sandbox-identity"] = string(data)
			p.Status.Phase = corev1.PodRunning
			namespacedPut(t, c, p)
			if _, err = s.ValidateForExec(ctx, id, uid); err == nil {
				t.Fatal("mutated immutable identity allowed")
			}
			observer, err := gates.NewNamespacedSandboxObserver(j, c.CoreV1().Pods(id.Namespace), namespacedConfig())
			sandboxOK(t, err)
			namespacedTerminal(p)
			namespacedPut(t, c, p)
			if err = observer.Observe(ctx, id); err == nil {
				t.Fatal("mutated immutable identity settled")
			}
		})
	}
	sandboxCount(t, pool, target, 1)
}

func TestNamespacedSandboxUIDCommitFailure(t *testing.T) {
	j, pool, target := journalFor(t, 1)
	ctx := scopedCtx(uuid.New())
	c := namespacedClient(t)
	// A real deferred-trigger COMMIT failure, not a lost COMMIT acknowledgement.
	// The wrapper creates this random owned DB; never install on shared datastores.
	// This test installs a constraint trigger on the service's own table. On the
	// shared dev datastore that would change every concurrently running suite's
	// behaviour and would outlive a crashed run, so it only runs against a
	// database this run exclusively owns. hack/owned-db-test.sh creates one.
	if !strings.HasPrefix(pool.Config().ConnConfig.Database, "nf_ci_recovery_") {
		t.Skip("fault injection needs an owned database: run ./hack/owned-db-test.sh")
	}
	intent := sandboxIntent()
	name := "namespaced_uid_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION gates.%s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.id = '%s'::uuid AND NEW.pod_uid IS NOT NULL THEN RAISE EXCEPTION 'owned UID commit failure'; END IF; RETURN NEW; END $$;
CREATE CONSTRAINT TRIGGER %s AFTER UPDATE ON gates.sandbox_invocations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION gates.%s()`, name, intent.InvocationID, name, name))
	sandboxOK(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER %s ON gates.sandbox_invocations; DROP FUNCTION gates.%s()", name, name))
		if err != nil {
			t.Error(err)
		}
	})
	c.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		// Read the actual journal at the side-effect boundary.
		v, err := j.Reserve(ctx, intent)
		if err != nil {
			t.Error(err)
			return true, nil, err
		}
		if v.CreateClaim == nil {
			t.Error("Create before acknowledged durable claim")
		}
		return false, nil, nil
	})
	s, err := newNamespacedFor(t, j, c)
	sandboxOK(t, err)
	id, uid, err := s.Allocate(ctx, intent)
	if err == nil || uid != "" {
		t.Fatal("uncommitted UID exposed")
	}
	v, err := j.ReadExact(ctx, id)
	sandboxOK(t, err)
	if v.PodUID != nil || v.CreateClaim == nil || v.Released {
		t.Fatal("UID failure lost obligation")
	}
	_, err = c.CoreV1().Pods(id.Namespace).Get(ctx, id.PodName, metav1.GetOptions{})
	sandboxOK(t, err)
	_, _, err = s.Allocate(ctx, intent)
	sandboxError(t, err, gates.ErrSandboxClaimed)
	sandboxCount(t, pool, target, 1)
}

func TestNamespacedSandboxDurableCleanupReopen(t *testing.T) {
	for _, boundary := range []string{"terminal", "cleanup", "deleted-without-ack"} {
		t.Run(boundary, func(t *testing.T) {
			j, pool, target := journalFor(t, 1)
			ctx := scopedCtx(uuid.New())
			c := namespacedClient(t)
			s, err := newNamespacedFor(t, j, c)
			sandboxOK(t, err)
			id, _, err := s.Allocate(ctx, sandboxIntent())
			sandboxOK(t, err)
			p, err := c.CoreV1().Pods(id.Namespace).Get(ctx, id.PodName, metav1.GetOptions{})
			sandboxOK(t, err)
			namespacedTerminal(p)
			namespacedPut(t, c, p)
			blocked := true
			c.PrependReactor("delete", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
				if !blocked {
					return false, nil, nil
				}
				if boundary == "terminal" {
					return true, nil, fmt.Errorf("delete unavailable")
				}
				if boundary == "deleted-without-ack" {
					_ = c.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), id.Namespace, id.PodName)
					return true, nil, context.DeadlineExceeded
				}
				return true, nil, nil // acknowledged but held by API
			})
			observer, err := gates.NewNamespacedSandboxObserver(j, c.CoreV1().Pods(id.Namespace), namespacedConfig())
			sandboxOK(t, err)
			if err = observer.Observe(ctx, id); err == nil {
				t.Fatal("unresolved boundary settled")
			}
			before, err := j.ReadExact(ctx, id)
			sandboxOK(t, err)
			if before.Terminal == nil {
				t.Fatal("terminal receipt missing")
			}
			// A fresh store/observer over durable rows, not an actual process restart.
			reopened, err := gates.NewSandboxJournal(context.Background(), pool, target, id.Namespace, 1)
			sandboxOK(t, err)
			observer, err = gates.NewNamespacedSandboxObserver(reopened, c.CoreV1().Pods(id.Namespace), namespacedConfig())
			sandboxOK(t, err)
			blocked = false
			if boundary == "cleanup" {
				sandboxOK(t, c.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), id.Namespace, id.PodName))
			}
			err = observer.Observe(ctx, id)
			if boundary == "deleted-without-ack" {
				if err == nil {
					t.Fatal("unacknowledged deletion released capacity")
				}
				sandboxCount(t, pool, target, 1)
			} else {
				sandboxOK(t, err)
				sandboxCount(t, pool, target, 0)
			}
			after, err := j.ReadExact(ctx, id)
			sandboxOK(t, err)
			if !reflect.DeepEqual(before.Terminal, after.Terminal) {
				t.Fatal("durable terminal receipt changed")
			}
			if before.Cleanup != nil && !reflect.DeepEqual(before.Cleanup, after.Cleanup) {
				t.Fatal("durable cleanup receipt changed")
			}
		})
	}
}

func TestNamespacedSandboxConcurrentAllocation(t *testing.T) {
	j, pool, target := journalFor(t, 1)
	ctx := scopedCtx(uuid.New())
	c := namespacedClient(t)
	s, err := newNamespacedFor(t, j, c)
	sandboxOK(t, err)
	intent := sandboxIntent()
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.Allocate(ctx, intent)
			if err != nil && err != gates.ErrSandboxClaimed {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(c.Actions()) != 1 || c.Actions()[0].GetVerb() != "create" {
		t.Fatal("concurrent exact retry dispatched more than once")
	}
	sandboxCount(t, pool, target, 1)
}

func TestNamespacedSandboxTerminalEvidenceRequired(t *testing.T) {
	j, pool, target := journalFor(t, 1)
	ctx := scopedCtx(uuid.New())
	c := namespacedClient(t)
	s, err := newNamespacedFor(t, j, c)
	sandboxOK(t, err)
	id, _, err := s.Allocate(ctx, sandboxIntent())
	sandboxOK(t, err)
	p, err := c.CoreV1().Pods(id.Namespace).Get(ctx, id.PodName, metav1.GetOptions{})
	sandboxOK(t, err)
	observer, err := gates.NewNamespacedSandboxObserver(j, c.CoreV1().Pods(id.Namespace), namespacedConfig())
	sandboxOK(t, err)
	for _, mode := range []string{"phase-only", "running", "zero-finish", "wrong-status", "conflicting-state", "status-image", "init-status", "ephemeral-status"} {
		t.Run(mode, func(t *testing.T) {
			q := p.DeepCopy()
			namespacedTerminal(q)
			switch mode {
			case "phase-only":
				q.Status.ContainerStatuses = nil
			case "running":
				q.Status.Phase = corev1.PodRunning
			case "zero-finish":
				q.Status.ContainerStatuses[0].State.Terminated.FinishedAt = metav1.Time{}
			case "wrong-status":
				q.Status.ContainerStatuses[0].Name = "wrong"
			case "conflicting-state":
				q.Status.ContainerStatuses[0].State.Running = &corev1.ContainerStateRunning{}
			case "status-image":
				q.Status.ContainerStatuses[0].Image = "wrong:latest"
			case "init-status":
				q.Status.InitContainerStatuses = q.Status.ContainerStatuses
			case "ephemeral-status":
				q.Status.EphemeralContainerStatuses = q.Status.ContainerStatuses
			}
			namespacedPut(t, c, q)
			c.ClearActions()
			if err := observer.Observe(ctx, id); err == nil {
				t.Fatal("incomplete terminal evidence settled")
			}
			for _, a := range c.Actions() {
				if a.GetVerb() != "get" {
					t.Fatal("deleted without terminal evidence")
				}
			}
			v, err := j.ReadExact(ctx, id)
			sandboxOK(t, err)
			if v.Terminal != nil || v.Released {
				t.Fatal("invented terminal evidence")
			}
		})
	}
	sandboxCount(t, pool, target, 1)
}

func TestNamespacedSandboxAlreadyExistsNoAuthority(t *testing.T) {
	j, pool, target := journalFor(t, 1)
	ctx := scopedCtx(uuid.New())
	c := namespacedClient(t)
	c.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "pods"}, a.(ktesting.CreateAction).GetObject().(*corev1.Pod).Name)
	})
	s, err := newNamespacedFor(t, j, c)
	sandboxOK(t, err)
	intent := sandboxIntent()
	id, uid, err := s.Allocate(ctx, intent)
	if !apierrors.IsAlreadyExists(err) || uid != "" {
		t.Fatal("AlreadyExists supplied authority")
	}
	_, _, err = s.Allocate(ctx, intent)
	sandboxError(t, err, gates.ErrSandboxClaimed)
	v, err := j.ReadExact(ctx, id)
	sandboxOK(t, err)
	if v.PodUID != nil || v.Released {
		t.Fatal("AlreadyExists settled obligation")
	}
	if len(c.Actions()) != 1 {
		t.Fatal("Create was replayed or readback attempted for authority")
	}
	sandboxCount(t, pool, target, 1)
}
