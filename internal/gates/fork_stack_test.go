package gates_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
)

// This uses the production service constructors and a real Kubernetes analysis
// pod. A refusal alone cannot prove fork support: the failing change must name
// the parent's gate, and fixing it must produce a successful reviewed merge.
func TestCrossForkExecutableGate(t *testing.T) {
	s := newPlatformStack(t)
	seedCalc(s)
	parentHead := s.head("main")
	fork, err := s.git.ForkRepo(s.as(s.author), &gitv1.ForkRepoRequest{Repo: s.repoID, Name: "fork-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatal(err)
	}
	forkID := fork.GetRepo().GetId()
	commit := func(content, policy string) string {
		t.Helper()
		r, err := s.git.CreateCommit(s.as(s.author), &gitv1.CreateCommitRequest{
			Repo: forkID, Branch: "main", Message: "fork change",
			Files: []*gitv1.FileChange{{Path: "calc.go", Content: []byte(content)}, {Path: ".novaforge/gates/tests.yaml", Content: []byte(policy)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return r.GetSha()
	}
	bad := commit(strings.Replace(addGo, "a + b", "a - b", 1), "name: tests\nrequired: false\n")
	r, err := s.reviews.CreateRun(s.as(s.author), &reviewsv1.CreateRunRequest{
		RepoId: s.repoID, SourceRepoId: forkID, Title: "fork contribution", SourceRef: "main", TargetRef: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	runID := r.GetRun().GetId()
	approve := func(sha string) {
		t.Helper()
		_, err := s.reviews.SubmitReview(s.as(s.member), &reviewsv1.SubmitReviewRequest{
			RunId: runID, Verdict: "approve", ExpectedSourceSha: sha,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	approve(bad)
	if _, err := s.merge(s.member, runID); err == nil || !strings.Contains(err.Error(), `gate "tests" status "fail"`) {
		t.Fatalf("fork must fail the parent's executable tests gate: %v", err)
	}
	if s.head("main") != parentHead {
		t.Fatal("refused fork changed parent history")
	}
	good := commit(addGo+"\n// fixed in the fork\n", "name: tests\nrequired: true\n")
	if _, err := s.merge(s.member, runID); err == nil {
		t.Fatal("stale review approved changed fork")
	}
	approve(good)
	if _, err := s.merge(s.member, runID); err != nil {
		t.Fatalf("passing fork cannot merge: %v", err)
	}
	if s.head("main") == parentHead {
		t.Fatal("successful merge did not update parent")
	}
}
