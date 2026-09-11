package reviews_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/reviews"
	"google.golang.org/grpc"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

// stubGitClient implements gitv1.GitServiceClient, returning a fixed diff
// from GetDiff and panicking if any other method is called.
type stubGitClient struct {
	gitv1.GitServiceClient
	diff string
}

func (s *stubGitClient) GetDiff(ctx context.Context, in *gitv1.GetDiffRequest, opts ...grpc.CallOption) (*gitv1.GetDiffResponse, error) {
	return &gitv1.GetDiffResponse{Unified: s.diff}, nil
}

const twoFileDiff = `diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 package foo
+// added line one
 func Foo() {}
-// removed line
diff --git a/bar.go b/bar.go
index 3333333..4444444 100644
--- a/bar.go
+++ b/bar.go
@@ -1,2 +1,3 @@
 package bar
+// added line two
+// added line three
`

func TestComputeImpactCountsFiles(t *testing.T) {
	client := &stubGitClient{diff: twoFileDiff}
	impact, err := reviews.ComputeImpact(context.Background(), client, uuid.New(), uuid.New(), "main", "feature")
	if err != nil {
		t.Fatalf("ComputeImpact: %v", err)
	}
	if impact.FilesChanged != 2 {
		t.Fatalf("want FilesChanged 2, got %d", impact.FilesChanged)
	}
	if impact.Insertions != 3 {
		t.Fatalf("want Insertions 3, got %d", impact.Insertions)
	}
	if impact.Deletions != 1 {
		t.Fatalf("want Deletions 1, got %d", impact.Deletions)
	}
	wantPaths := []string{"foo.go", "bar.go"}
	if len(impact.Paths) != len(wantPaths) {
		t.Fatalf("want %d paths, got %d: %v", len(wantPaths), len(impact.Paths), impact.Paths)
	}
	for i, p := range wantPaths {
		if impact.Paths[i] != p {
			t.Fatalf("want path[%d] = %q, got %q", i, p, impact.Paths[i])
		}
	}
}

func TestComputeImpactEmptyDiff(t *testing.T) {
	client := &stubGitClient{diff: ""}
	impact, err := reviews.ComputeImpact(context.Background(), client, uuid.New(), uuid.New(), "main", "feature")
	if err != nil {
		t.Fatalf("ComputeImpact: %v", err)
	}
	if impact.FilesChanged != 0 || impact.Insertions != 0 || impact.Deletions != 0 || len(impact.Paths) != 0 {
		t.Fatalf("want zero-valued Impact, got %+v", impact)
	}
}
