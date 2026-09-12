package workspace_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
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
				"novaforge.io/created-at": strconv.FormatInt(oldCreated.UTC().Unix(), 10),
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

// TestProvisionedLabelsAreLegal pins a defect the fake clientset can never
// catch: it does not validate label syntax, so every unit test passed while
// the real API server rejected every namespace this package created. The
// created-at label held an RFC 3339 timestamp, and a label value may not
// contain a colon — no agent workspace could be provisioned at all.
//
// This asserts the labels against Kubernetes' own validation rather than
// against a guess at them.
func TestProvisionedLabelsAreLegal(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	p := workspace.NewProvisioner(clientset)
	runID := uuid.New()

	if _, err := p.Create(context.Background(), runID, workspace.Spec{Image: "busybox"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	ns, err := clientset.CoreV1().Namespaces().Get(context.Background(), "nf-run-"+runID.String(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if len(ns.Labels) == 0 {
		t.Fatal("the namespace carries no labels at all")
	}
	for k, v := range ns.Labels {
		if errs := validation.IsQualifiedName(k); len(errs) > 0 {
			t.Errorf("label key %q is not a legal Kubernetes label name: %v", k, errs)
		}
		if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
			t.Errorf("label %s=%q is not a legal Kubernetes label value: %v — the API server will reject this object", k, v, errs)
		}
	}
}

// TestProvisionedPodIsStructurallyValid pins the second defect the fake
// clientset accepted and the API server refused: the pod declared a repo
// volume unconditionally, so with no claim supplied its
// persistentVolumeClaim.claimName was empty and its volumeMount referenced
// a volume that could not exist. No agent pod could start.
//
// The fake performs no schema validation at all, so these two invariants —
// every mount names a declared volume, and no PVC volume has an empty claim
// — are asserted directly.
func TestProvisionedPodIsStructurallyValid(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	p := workspace.NewProvisioner(clientset)
	runID := uuid.New()

	ws, err := p.Create(context.Background(), runID, workspace.Spec{Image: "busybox"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	pod, err := clientset.CoreV1().Pods("nf-run-"+runID.String()).Get(context.Background(), ws.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}

	declared := map[string]bool{}
	for _, v := range pod.Spec.Volumes {
		declared[v.Name] = true
		if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == "" {
			t.Errorf("volume %q is a persistentVolumeClaim with an empty claimName; the API server refuses this pod", v.Name)
		}
	}
	for _, c := range pod.Spec.Containers {
		for _, m := range c.VolumeMounts {
			if !declared[m.Name] {
				t.Errorf("container %q mounts volume %q, which the pod does not declare", c.Name, m.Name)
			}
		}
	}
	if len(pod.Spec.Volumes) == 0 {
		t.Error("the workspace has no writable volume of its own")
	}
}

// TestProvisionedPodMountsASuppliedRepoClaim pins that a caller who does
// supply a claim still gets the repository mounted, read-only.
func TestProvisionedPodMountsASuppliedRepoClaim(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	p := workspace.NewProvisioner(clientset)
	runID := uuid.New()

	ws, err := p.Create(context.Background(), runID, workspace.Spec{Image: "busybox", RepoPVC: "repo-claim"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pod, err := clientset.CoreV1().Pods("nf-run-"+runID.String()).Get(context.Background(), ws.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}

	var found bool
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == "repo-claim" {
			found = true
			if !v.PersistentVolumeClaim.ReadOnly {
				t.Error("the repository is mounted writable; an agent writes through the platform, not the volume")
			}
		}
	}
	if !found {
		t.Fatal("the supplied repository claim is not mounted")
	}
}
