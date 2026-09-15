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

// TestAssessRiskSaysWhy pins the risk rules and that each level carries the
// rule that set it: a bare level is a number a reviewer must take on trust.
func TestAssessRiskSaysWhy(t *testing.T) {
	for _, c := range []struct {
		name string
		im   reviews.Impact
		want string
	}{
		{"small", reviews.Impact{FilesChanged: 2, Insertions: 10, Paths: []string{"api/a.go", "api/b.go"}}, "low"},
		{"many files", reviews.Impact{FilesChanged: 8, Insertions: 40, Paths: []string{"a.go"}}, "medium"},
		{"many lines", reviews.Impact{FilesChanged: 1, Insertions: 300, Paths: []string{"a.go"}}, "medium"},
		{"huge", reviews.Impact{FilesChanged: 30, Insertions: 10, Paths: []string{"a.go"}}, "high"},
		{"migration", reviews.Impact{FilesChanged: 1, Insertions: 3, Paths: []string{"internal/work/migrations/000008_x.up.sql"}}, "high"},
		{"gate config", reviews.Impact{FilesChanged: 1, Insertions: 1, Paths: []string{".novaforge/gates/tests.yaml"}}, "high"},
		{"dependency", reviews.Impact{FilesChanged: 1, Insertions: 1, Paths: []string{"go.mod"}}, "high"},
	} {
		got, reasons := reviews.AssessRisk(c.im)
		if got != c.want || len(reasons) == 0 || reasons[0] == "" {
			t.Errorf("%s: risk %q %v, want %q with a reason", c.name, got, reasons, c.want)
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
