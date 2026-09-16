package runner_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/runner"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// A successful command is not a successful CI job if its required evidence
// cannot be retained. Session maps an executor error to a failed job status.
func TestPodArtifactFailureFailsJob(t *testing.T) {
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "report.txt", Mode: 0o600, Size: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	valid := "::novaforge-artifacts-begin::\n" + base64.StdEncoding.EncodeToString(archive.Bytes()) + "\n::novaforge-artifacts-end::\n"
	for _, tc := range []struct {
		name, output, want string
		uploadErr          error
	}{
		{"upload unavailable", valid, "upload", errors.New("object storage unavailable")},
		{"missing evidence", "command succeeded\n", "artifact", nil},
		{"truncated evidence", strings.TrimSuffix(valid, "::novaforge-artifacts-end::\n"), "artifact", nil},
		{"corrupt evidence", "::novaforge-artifacts-begin::\ninvalid!\n::novaforge-artifacts-end::\n", "artifact", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewSimpleClientset()
			cs.PrependReactor("get", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
				if ga, ok := a.(k8stesting.GenericActionImpl); ok && ga.GetSubresource() == "log" {
					return true, &runtime.Unknown{Raw: []byte(tc.output)}, nil
				}
				return true, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "nf-job-artifact"}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}, nil
			})
			px := &runner.PodExecutor{Client: cs, Namespace: "novaforge", OnArtifacts: func(context.Context, string, []runner.Artifact) error { return tc.uploadErr }}
			_, err := px.Run(context.Background(), &civ1.ConnectResponse{JobId: "artifact", RunCmd: "true", ArtifactPaths: []string{"report.txt"}}, make(chan string, 16))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("job error = %v, want %s failure", err, tc.want)
			}
		})
	}
}
