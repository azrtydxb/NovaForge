package service_test

import (
	"strings"
	"testing"
)

// TestOKEReachesOCIObjectStorageNotABundledMinIO pins the OKE object store.
// MinIO stopped publishing community images, so the bundled one could not be
// pulled on OKE, and git-platform and ci-runner exit on start when they
// cannot reach their bucket. OCI Object Storage's S3 API answers only over
// HTTPS and only to requests signed for its region.
func TestOKEReachesOCIObjectStorageNotABundledMinIO(t *testing.T) {
	objects := decodeManifests(t, renderOKE(t))
	for _, o := range objects {
		if strings.Contains(o.Metadata.Name, "minio") {
			t.Errorf("OKE renders %s %s", o.Kind, o.Metadata.Name)
		}
	}
	for _, svc := range []string{"git-platform", "ci-runner"} {
		d := find(objects, "Deployment", okeRelease+"-"+svc)
		if d == nil {
			t.Fatalf("no %s Deployment rendered", svc)
		}
		env := map[string]string{}
		for _, e := range d.Spec.Template.Spec.Containers[0].Env {
			env[e.Name] = e.Value
		}
		if env["S3_USE_SSL"] != "true" {
			t.Errorf("%s: S3_USE_SSL = %q, want \"true\"", svc, env["S3_USE_SSL"])
		}
		if env["S3_REGION"] != "me-dubai-1" {
			t.Errorf("%s: S3_REGION = %q, want \"me-dubai-1\"", svc, env["S3_REGION"])
		}
	}
}
