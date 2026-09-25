package workspace_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/workspace"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/fake"
)

func TestWorkspaceConfinement(t *testing.T) {
	client := fake.NewClientset()
	p := workspace.NewProvisioner(client)
	ctx := context.Background()
	ws, err := p.Create(ctx, uuid.New(), workspace.Spec{Image: workspace.DefaultImage})
	if err != nil {
		t.Fatal(err)
	}
	if errs := validation.IsDNS1123Label(ws.Namespace); len(errs) > 0 {
		t.Fatal(errs)
	}
	policy, err := client.NetworkingV1().NetworkPolicies(ws.Namespace).Get(ctx, "default-deny-all", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	types := map[networkingv1.PolicyType]bool{}
	for _, kind := range policy.Spec.PolicyTypes {
		types[kind] = true
	}
	if !types[networkingv1.PolicyTypeIngress] || !types[networkingv1.PolicyTypeEgress] || len(policy.Spec.Egress) != 0 || len(policy.Spec.Ingress) != 0 {
		t.Errorf("policy is not deny-all: %+v", policy.Spec)
	}
	pod, err := client.CoreV1().Pods(ws.Namespace).Get(ctx, ws.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("workspace receives a cluster credential")
	}
	for _, c := range pod.Spec.Containers {
		if c.SecurityContext == nil || c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation || c.SecurityContext.Capabilities == nil || len(c.SecurityContext.Capabilities.Drop) != 1 || c.SecurityContext.Capabilities.Drop[0] != corev1.Capability("ALL") {
			t.Error("workspace permits privilege escalation or retains capabilities")
		}
	}
}
