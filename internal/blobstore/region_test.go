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
		w.WriteHeader(http.StatusOK)
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
