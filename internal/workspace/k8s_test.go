package workspace_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/novaforge/novaforge/internal/workspace"
)

func testSpec() workspace.Spec {
	return workspace.Spec{
		Image:    "novaforge/agent-runner:latest",
		Env:      map[string]string{"NOVAFORGE_RUN": "1"},
		CPULimit: "500m",
		MemLimit: "512Mi",
		RepoPVC:  "repo-pvc",
	}
}

func TestCreateMakesNamespacedPod(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	prov := workspace.NewProvisioner(clientset)
	runID := uuid.New()

	ws, err := prov.Create(context.Background(), runID, testSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	wantNS := "nf-run-" + runID.String()
	if ws.Namespace != wantNS {
		t.Fatalf("want namespace %q, got %q", wantNS, ws.Namespace)
	}

	ns, err := clientset.CoreV1().Namespaces().Get(context.Background(), wantNS, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if ns.Labels["novaforge.io/run-id"] != runID.String() {
		t.Fatalf("want namespace label novaforge.io/run-id=%s, got %q", runID, ns.Labels["novaforge.io/run-id"])
	}

	pod, err := clientset.CoreV1().Pods(wantNS).Get(context.Background(), ws.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if pod.Labels["novaforge.io/run-id"] != runID.String() {
		t.Fatalf("want pod label novaforge.io/run-id=%s, got %q", runID, pod.Labels["novaforge.io/run-id"])
	}
}

func TestCreateAppliesDenyAllNetworkPolicy(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	prov := workspace.NewProvisioner(clientset)
	runID := uuid.New()

	ws, err := prov.Create(context.Background(), runID, testSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	list, err := clientset.NetworkingV1().NetworkPolicies(ws.Namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list network policies: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("want 1 network policy, got %d", len(list.Items))
	}
	np := list.Items[0]
	if len(np.Spec.PodSelector.MatchLabels) != 0 || len(np.Spec.PodSelector.MatchExpressions) != 0 {
		t.Fatalf("want empty pod selector (applies to all pods), got %+v", np.Spec.PodSelector)
	}
	if len(np.Spec.Ingress) != 0 {
		t.Fatalf("want no ingress rules, got %d", len(np.Spec.Ingress))
	}
	foundIngress := false
	for _, t := range np.Spec.PolicyTypes {
		if t == networkingv1.PolicyTypeIngress {
			foundIngress = true
		}
	}
	if !foundIngress {
		t.Fatalf("want policy type Ingress present, got %v", np.Spec.PolicyTypes)
	}
}

func TestDestroyRemovesNamespace(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	prov := workspace.NewProvisioner(clientset)
	runID := uuid.New()

	ws, err := prov.Create(context.Background(), runID, testSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := prov.Destroy(context.Background(), runID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	_, err = clientset.CoreV1().Namespaces().Get(context.Background(), ws.Namespace, metav1.GetOptions{})
	if err == nil {
		t.Fatal("want namespace gone after Destroy, still present")
	}
}

func TestReapRemovesOrphanedNamespaces(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	prov := workspace.NewProvisioner(clientset)

	oldRunID := uuid.New()
	oldCreated := time.Now().Add(-2 * time.Hour)
	_, err := clientset.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nf-run-" + oldRunID.String(),
			Labels: map[string]string{
				"novaforge.io/run-id":     oldRunID.String(),
				"novaforge.io/created-at": oldCreated.UTC().Format(time.RFC3339),
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed old namespace: %v", err)
	}

	freshRunID := uuid.New()
	if _, err := prov.Create(context.Background(), freshRunID, testSpec()); err != nil {
		t.Fatalf("Create fresh: %v", err)
	}

	n, err := prov.Reap(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 reaped namespace, got %d", n)
	}

	_, err = clientset.CoreV1().Namespaces().Get(context.Background(), "nf-run-"+oldRunID.String(), metav1.GetOptions{})
	if err == nil {
		t.Fatal("want old namespace gone after Reap")
	}

	_, err = clientset.CoreV1().Namespaces().Get(context.Background(), "nf-run-"+freshRunID.String(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("want fresh namespace to survive Reap, got err: %v", err)
	}
}
