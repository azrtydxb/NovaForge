package service_test

import (
	"testing"

	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/service"
)

// TestBlobOptionsCarriesEveryConnectionField pins the defect that left
// object storage on plain HTTP: blobstore.Options had UseSSL, the callers
// built Options by hand, and none of them set it.
func TestBlobOptionsCarriesEveryConnectionField(t *testing.T) {
	cfg := service.Config{S3Endpoint: "ns.compat.objectstorage.me-dubai-1.oraclecloud.com",
		S3AccessKey: "k", S3SecretKey: "s", S3UseSSL: true, S3Region: "me-dubai-1"}
	got := cfg.BlobOptions("novaforge-ci")
	want := blobstore.Options{Endpoint: cfg.S3Endpoint, AccessKey: "k", SecretKey: "s",
		Bucket: "novaforge-ci", UseSSL: true, Region: "me-dubai-1"}
	if got != want {
		t.Fatalf("BlobOptions = %+v, want %+v", got, want)
	}
}
