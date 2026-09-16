package workspace_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/workspace"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCreateRecordsRunExpiry(t *testing.T) {
	cs := fake.NewSimpleClientset()
	p := workspace.NewProvisioner(cs)
	expiry := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
	spec := testSpec()
	spec.ExpiresAt = expiry
	ws, err := p.Create(context.Background(), uuid.New(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := cs.CoreV1().Namespaces().Get(context.Background(), ws.Namespace, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := ns.Annotations["novaforge.io/expires-at"]; got != expiry.Format(time.RFC3339) {
		t.Fatalf("recorded expiry = %q, want %s", got, expiry)
	}
}

func TestReaperHonorsRunExpiry(t *testing.T) {
	for _, tc := range []struct {
		name, expiry string
		want         int
	}{
		{"long running", time.Now().Add(time.Hour).UTC().Format(time.RFC3339), 0},
		{"expired", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), 1},
		{"legacy", "", 1},
		{"malformed", "not-a-time", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.New().String()
			cs := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name:        "nf-run-" + id,
				Labels:      map[string]string{"novaforge.io/run-id": id, "novaforge.io/created-at": strconv.FormatInt(time.Now().Add(-2*time.Hour).Unix(), 10)},
				Annotations: map[string]string{"novaforge.io/expires-at": tc.expiry},
			}})
			n, err := workspace.NewProvisioner(cs).Reap(context.Background(), time.Hour)
			if err != nil || n != tc.want {
				t.Fatalf("Reap = %d, %v; want %d", n, err, tc.want)
			}
		})
	}
}
