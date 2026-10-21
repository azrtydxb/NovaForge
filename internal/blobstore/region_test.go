package blobstore_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/novaforge/novaforge/internal/blobstore"
)

// TestRegionSignsRequestsWithoutAskingTheEndpoint pins the seam OCI Object
// Storage depends on: it signs for its own region, so a client that signs for
// another, or first asks for the bucket's location, is refused. The server
// records what the client actually sent, rather than trusting that a field on
// Options reached the wire.
func TestRegionSignsRequestsWithoutAskingTheEndpoint(t *testing.T) {
	var mu sync.Mutex
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Clone(context.Background()))
		mu.Unlock()
		listBucket(w, r)
	}))
	defer srv.Close()

	_, err := blobstore.New(context.Background(), blobstore.Options{
		Endpoint:  strings.TrimPrefix(srv.URL, "http://"),
		AccessKey: "k",
		SecretKey: "s",
		Bucket:    "novaforge-ci",
		Region:    "me-dubai-1",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("the client sent nothing")
	}
	for _, r := range seen {
		if _, ok := r.URL.Query()["location"]; ok {
			t.Errorf("%s %s asked the endpoint for the bucket location although Region was given", r.Method, r.URL)
		}
		if auth := r.Header.Get("Authorization"); !strings.Contains(auth, "/me-dubai-1/s3/aws4_request") {
			t.Errorf("%s %s is not signed for me-dubai-1: %q", r.Method, r.URL, auth)
		}
	}
}

// listBucket answers the one request New needs, an object listing of an empty
// bucket, and refuses everything else the way a bucket-scoped OCI key does.
func listBucket(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>novaforge-ci</Name><KeyCount>0</KeyCount><MaxKeys>1</MaxKeys><IsTruncated>false</IsTruncated></ListBucketResult>`))
		return
	}
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>Access Denied.</Message></Error>`))
}

// TestNewNeedsOnlyObjectAccessToAnExistingBucket pins what OCI Object Storage
// allows a key scoped to its buckets ("read buckets" and "manage objects" where
// target.bucket.name is the bucket): listing objects, but not HEAD on the
// bucket. New used BucketExists, a HEAD, so git-platform and ci-runner were
// refused at start ("check bucket novaforge-ci: Access Denied") with a key
// that could read and write every object they need.
func TestNewNeedsOnlyObjectAccessToAnExistingBucket(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(listBucket))
	defer srv.Close()
	_, err := blobstore.New(context.Background(), blobstore.Options{
		Endpoint: strings.TrimPrefix(srv.URL, "http://"), AccessKey: "k", SecretKey: "s",
		Bucket: "novaforge-ci", Region: "me-dubai-1",
	})
	if err != nil {
		t.Fatalf("New with object access to an existing bucket: %v", err)
	}
}
