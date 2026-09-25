package workspace_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/workspace"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDestroyConfirmedRequiresOwnedTerminationEvidence(t *testing.T) {
	for _, scenario := range []string{"terminated", "replacement-namespace", "replacement-pod", "missing-unknown", "restart-recorded"} {
		t.Run(scenario, func(t *testing.T) {
			org, run := uuid.New(), uuid.New()
			ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorID: uuid.New(), ActorKind: "agent"})
			id := workspace.Identity{OrgID: org, RunID: run, NamespaceUID: types.UID(uuid.NewString()), PodUID: types.UID(uuid.NewString()), NodeName: "node"}
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nf-run-" + run.String(), UID: id.NamespaceUID, Labels: map[string]string{"novaforge.io/org-id": org.String(), "novaforge.io/run-id": run.String()}}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: ns.Name, UID: id.PodUID}, Spec: corev1.PodSpec{NodeName: "node", Containers: []corev1.Container{{Name: "agent"}}}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{Name: "agent", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}}}
			client := fake.NewClientset(ns, pod)
			if scenario == "replacement-namespace" {
				id.NamespaceUID = "not-the-namespace"
			}
			if scenario == "replacement-pod" {
				id.PodUID = "not-the-pod"
			}
			if scenario == "missing-unknown" || scenario == "restart-recorded" {
				if err := client.CoreV1().Pods(ns.Name).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "restart-recorded" {
				id.TerminationObserved = true
			}
			var recorded atomic.Bool
			p := workspace.NewProvisioner(client).WithCleanupRecorder(func(_ context.Context, observed workspace.Identity) error {
				recorded.Store(observed.TerminationObserved)
				return nil
			})
			err := p.DestroyConfirmed(ctx, id)
			succeeds := scenario == "terminated" || scenario == "restart-recorded"
			if (err == nil) != succeeds {
				t.Fatalf("cleanup=%v expected success=%v", err, succeeds)
			}
			if scenario == "terminated" && !recorded.Load() {
				t.Fatal("termination not recorded before deletion")
			}
			if _, err = p.Exec(ctx, run, []string{"true"}, nil); !errors.Is(err, workspace.ErrWorkspaceClosed) {
				t.Fatal("workspace reused after teardown", err)
			}
			if !succeeds {
				if _, err = client.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{}); err != nil {
					t.Fatal("unconfirmed/replacement namespace deleted")
				}
			}
		})
	}
}
