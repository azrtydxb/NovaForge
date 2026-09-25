package gates_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/gates"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func namespacedConfig() gates.NamespacedSandboxConfig {
	return gates.NamespacedSandboxConfig{Image: "registry.invalid/analysis@sha256:" + strings.Repeat("d", 64), ApplicationNamespace: "novaforge", ServiceAccount: "gate-sandbox"}
}

func namespacedClient(t *testing.T) *fake.Clientset {
	t.Helper()
	c := fake.NewSimpleClientset()
	c.PrependReactor("*", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetResource().Resource != "pods" || a.GetNamespace() != "gate-sandbox" || a.GetSubresource() != "" || (a.GetVerb() != "create" && a.GetVerb() != "get" && a.GetVerb() != "delete") {
			t.Errorf("forbidden Kubernetes action: %s %s/%s", a.GetVerb(), a.GetResource().Resource, a.GetSubresource())
			return true, nil, fmt.Errorf("forbidden action")
		}
		return false, nil, nil
	})
	c.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		p := a.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		p.UID = types.UID(uuid.NewString())
		return false, nil, nil
	})
	return c
}

func namespacedTerminal(p *corev1.Pod) {
	p.Status.Phase = corev1.PodSucceeded
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "analysis", Image: p.Spec.Containers[0].Image, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, FinishedAt: metav1.NewTime(time.Now().Add(-time.Second))}}}}
}

func namespacedPut(t *testing.T, c *fake.Clientset, p *corev1.Pod) {
	t.Helper()
	sandboxOK(t, c.Tracker().Update(corev1.SchemeGroupVersion.WithResource("pods"), p, p.Namespace))
}

func TestNamespacedSandboxLifecycle(t *testing.T) {
	j, pool, target := journalFor(t, 1)
	ctx := scopedCtx(uuid.New())
	c := namespacedClient(t)
	s, err := newNamespacedFor(t, j, c)
	sandboxOK(t, err)
	intent := sandboxIntent()
	id, uid, err := s.Allocate(ctx, intent)
	sandboxOK(t, err)
	if uid == "" {
		t.Fatal("missing bound UID")
	}
	v, err := j.ReadExact(ctx, id)
	sandboxOK(t, err)
	if v.CreateClaim == nil || v.PodUID == nil || *v.PodUID != uid {
		t.Fatal("usable identity before durable claims/UID")
	}
	_, _, err = s.Allocate(ctx, intent)
	sandboxError(t, err, gates.ErrSandboxClaimed)
	sandboxCount(t, pool, target, 1)
	p, err := c.CoreV1().Pods(id.Namespace).Get(ctx, id.PodName, metav1.GetOptions{})
	sandboxOK(t, err)
	if len(validation.IsDNS1123Subdomain(p.Name)) != 0 {
		t.Fatal("invalid pod name")
	}
	for k, v := range p.Labels {
		if len(validation.IsQualifiedName(k)) != 0 || len(validation.IsValidLabelValue(v)) != 0 {
			t.Fatalf("invalid label %s=%s", k, v)
		}
	}
	for k := range p.Annotations {
		if len(validation.IsQualifiedName(k)) != 0 {
			t.Fatal("invalid annotation key")
		}
	}
	if p.Spec.AutomountServiceAccountToken == nil || *p.Spec.AutomountServiceAccountToken || len(p.Spec.Volumes) != 2 || p.Spec.ServiceAccountName != "gate-sandbox" {
		t.Fatal("unsafe pod profile")
	}
	p.Status.Phase = corev1.PodRunning
	namespacedPut(t, c, p)
	_, err = j.ClaimExec(ctx, id, uid)
	sandboxOK(t, err)
	_, err = s.ValidateForExec(ctx, id, uid)
	sandboxOK(t, err)
	namespacedTerminal(p)
	namespacedPut(t, c, p)
	// Delete reactor checks the real journal at the side-effect boundary.
	c.PrependReactor("delete", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		before, e := j.ReadExact(ctx, id)
		sandboxOK(t, e)
		if before.Terminal == nil {
			t.Fatal("Delete before durable terminal")
		}
		opts := a.(ktesting.DeleteAction).GetDeleteOptions()
		if opts.Preconditions == nil || opts.Preconditions.UID == nil || string(*opts.Preconditions.UID) != uid {
			t.Fatal("Delete without exact UID precondition")
		}
		return false, nil, nil
	})
	observer, err := gates.NewNamespacedSandboxObserver(j, c.CoreV1().Pods(id.Namespace), namespacedConfig())
	sandboxOK(t, err)
	sandboxOK(t, observer.Observe(ctx, id))
	v, err = j.ReadExact(ctx, id)
	sandboxOK(t, err)
	if !v.Released || v.Terminal == nil || v.Cleanup == nil || v.Absence == nil || v.ToolResult != nil {
		t.Fatal("settlement or result provenance wrong")
	}
	sandboxCount(t, pool, target, 0)
	sandboxOK(t, observer.Observe(ctx, id))
	_, err = s.ValidateForExec(ctx, id, uid)
	if err == nil {
		t.Fatal("exec after settlement")
	}
	creates := 0
	for _, a := range c.Actions() {
		if a.GetVerb() == "create" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("Create calls=%d", creates)
	}
}

func TestNamespacedSandboxUncertainCreateLateArrival(t *testing.T) {
	j, pool, target := journalFor(t, 1)
	ctx := scopedCtx(uuid.New())
	c := namespacedClient(t)
	var delayed *corev1.Pod
	c.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		delayed = a.(ktesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
		delayed.UID = "late-uid"
		return true, nil, apierrors.NewTimeoutError("create acknowledgement lost", 0)
	})
	s, err := newNamespacedFor(t, j, c)
	sandboxOK(t, err)
	intent := sandboxIntent()
	id, uid, err := s.Allocate(ctx, intent)
	if !apierrors.IsTimeout(err) || uid != "" {
		t.Fatalf("uncertainty returned usable identity: %q %v", uid, err)
	}
	// Reopen durable journal, not a process restart or lost-COMMIT injection.
	reopened, err := gates.NewSandboxJournal(context.Background(), pool, target, "gate-sandbox", 1)
	sandboxOK(t, err)
	observer, err := gates.NewNamespacedSandboxObserver(reopened, c.CoreV1().Pods(id.Namespace), namespacedConfig())
	sandboxOK(t, err)
	if err = observer.Observe(ctx, id); err == nil {
		t.Fatal("NotFound settled uncertain Create")
	}
	sandboxCount(t, pool, target, 1)
	retry, err := newNamespacedFor(t, reopened, c)
	sandboxOK(t, err)
	_, _, err = retry.Allocate(ctx, intent)
	sandboxError(t, err, gates.ErrSandboxClaimed)
	_, _, err = retry.Allocate(ctx, sandboxIntent())
	sandboxError(t, err, gates.ErrSandboxCapacity)
	sandboxOK(t, c.Tracker().Create(corev1.SchemeGroupVersion.WithResource("pods"), delayed, id.Namespace))
	if err = observer.Observe(ctx, id); err == nil {
		t.Fatal("nonterminal late pod settled")
	}
	v, err := j.ReadExact(ctx, id)
	sandboxOK(t, err)
	if v.PodUID == nil || *v.PodUID != "late-uid" || v.Released {
		t.Fatal("late identity not retained")
	}
	namespacedTerminal(delayed)
	namespacedPut(t, c, delayed)
	sandboxOK(t, observer.Observe(ctx, id))
	sandboxCount(t, pool, target, 0)
	creates := 0
	for _, a := range c.Actions() {
		if a.GetVerb() == "create" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("uncertain Create replayed %d times", creates)
	}
}

func TestNamespacedSandboxMutationRefused(t *testing.T) {
	j, _, _ := journalFor(t, 1)
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
	original.Status.Phase = corev1.PodRunning
	mutations := map[string]func(*corev1.Pod){
		"uid": func(p *corev1.Pod) { p.UID = "replacement" },
		"identity": func(p *corev1.Pod) {
			for k := range p.Annotations {
				p.Annotations[k] += "mutated"
				break
			}
		},
		"container": func(p *corev1.Pod) { p.Spec.Containers[0].Name = "wrong" },
		"image":     func(p *corev1.Pod) { p.Spec.Containers[0].Image = "evil:latest" },
		"injected": func(p *corev1.Pod) {
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "injected", Image: namespacedConfig().Image})
		},
		"init": func(p *corev1.Pod) {
			p.Spec.InitContainers = []corev1.Container{{Name: "init", Image: namespacedConfig().Image}}
		},
		"ephemeral": func(p *corev1.Pod) {
			p.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Image: namespacedConfig().Image}}}
		},
		"token":           func(p *corev1.Pod) { p.Spec.AutomountServiceAccountToken = new(true) },
		"service-account": func(p *corev1.Pod) { p.Spec.ServiceAccountName = "privileged" },
		"secret": func(p *corev1.Pod) {
			p.Spec.Volumes[0].VolumeSource = corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "credential"}}
		},
		"env": func(p *corev1.Pod) {
			p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "TOKEN", Value: "injected"})
		},
		"command": func(p *corev1.Pod) { p.Spec.Containers[0].Command = []string{"evil"} },
		"projected-token": func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "token", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}}}}}})
		},
		"image-pull-credential": func(p *corev1.Pod) { p.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "credential"}} },
		"env-from": func(p *corev1.Pod) {
			p.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "credential"}}}}
		},
		"host-network": func(p *corev1.Pod) { p.Spec.HostNetwork = true },
		"privileged":   func(p *corev1.Pod) { p.Spec.Containers[0].SecurityContext.Privileged = new(true) },
		"labels":       func(p *corev1.Pod) { p.Labels["novaforge.io/gate-sandbox"] = "false" },
	}
	observer, err := gates.NewNamespacedSandboxObserver(j, c.CoreV1().Pods(id.Namespace), namespacedConfig())
	sandboxOK(t, err)
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			p := original.DeepCopy()
			mutate(p)
			namespacedPut(t, c, p)
			if _, err := s.ValidateForExec(ctx, id, uid); err == nil {
				t.Fatal("mutated pod allowed exec")
			}
			namespacedTerminal(p)
			namespacedPut(t, c, p)
			c.ClearActions()
			if err := observer.Observe(ctx, id); err == nil {
				t.Fatal("mutated pod settled")
			}
			for _, a := range c.Actions() {
				if a.GetVerb() != "get" {
					t.Fatalf("observer mutation action: %s", a.GetVerb())
				}
			}
		})
	}
}

func TestNamespacedSandboxCleanupRetainsCapacity(t *testing.T) {
	for _, mode := range []string{"held", "conflict", "missing", "delete-timeout", "delete-notfound"} {
		t.Run(mode, func(t *testing.T) {
			j, pool, target := journalFor(t, 1)
			ctx := scopedCtx(uuid.New())
			c := namespacedClient(t)
			s, err := newNamespacedFor(t, j, c)
			sandboxOK(t, err)
			id, _, err := s.Allocate(ctx, sandboxIntent())
			sandboxOK(t, err)
			p, err := c.CoreV1().Pods(id.Namespace).Get(ctx, id.PodName, metav1.GetOptions{})
			sandboxOK(t, err)
			if mode == "missing" {
				sandboxOK(t, c.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), id.Namespace, id.PodName))
			} else {
				namespacedTerminal(p)
				namespacedPut(t, c, p)
			}
			c.PrependReactor("delete", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
				switch mode {
				case "conflict":
					return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, id.PodName, fmt.Errorf("UID precondition"))
				case "delete-timeout":
					return true, nil, context.DeadlineExceeded
				case "delete-notfound":
					return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, id.PodName)
				}
				return true, nil, nil
			})
			observer, err := gates.NewNamespacedSandboxObserver(j, c.CoreV1().Pods(id.Namespace), namespacedConfig())
			sandboxOK(t, err)
			if err = observer.Observe(ctx, id); err == nil {
				t.Fatal("unresolved pod settled")
			}
			before, err := j.ReadExact(ctx, id)
			sandboxOK(t, err)
			sandboxCount(t, pool, target, 1)
			if before.Released || before.Absence != nil {
				t.Fatal("released unresolved obligation")
			}
			if mode == "held" && before.Cleanup == nil {
				t.Fatal("missing cleanup receipt")
			}
			if mode != "held" && before.Cleanup != nil {
				t.Fatal("unacknowledged delete receipt")
			}
			_ = observer.Observe(ctx, id)
			after, err := j.ReadExact(ctx, id)
			sandboxOK(t, err)
			if !reflect.DeepEqual(before.Terminal, after.Terminal) || !reflect.DeepEqual(before.Cleanup, after.Cleanup) {
				t.Fatal("reobservation replaced immutable receipts")
			}
		})
	}
}

func TestNamespacedSandboxFences(t *testing.T) {
	for _, orgFence := range []bool{false, true} {
		t.Run(fmt.Sprint(orgFence), func(t *testing.T) {
			j, pool, target := journalFor(t, 1)
			ctx := scopedCtx(uuid.New())
			intent := sandboxIntent()
			c := namespacedClient(t)
			if orgFence {
				_, err := j.FenceOrganization(ctx)
				sandboxOK(t, err)
			} else {
				_, err := j.FenceRuns(ctx, []uuid.UUID{intent.RunID})
				sandboxOK(t, err)
			}
			s, err := newNamespacedFor(t, j, c)
			sandboxOK(t, err)
			_, uid, err := s.Allocate(ctx, intent)
			sandboxError(t, err, gates.ErrSandboxFenced)
			if uid != "" || len(c.Actions()) != 0 {
				t.Fatal("fenced allocation caused effects")
			}
			sandboxCount(t, pool, target, 0)
		})
	}
}
