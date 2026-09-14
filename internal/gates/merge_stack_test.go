package gates_test

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const goMod = "module example.com/calc\n\ngo 1.22\n"

const addGo = "package calc\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int { return a + b }\n"

const addTest = "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add(2, 3) != 5\")\n\t}\n}\n"

// seedCalc gives the stack's repository a passing Go module on main, with the
// tests gate declared required.
func seedCalc(s *platformStack) {
	s.commit("main", "", map[string]*string{
		"go.mod":                      str(goMod),
		"calc.go":                     str(addGo),
		"calc_test.go":                str(addTest),
		".novaforge/gates/tests.yaml": str("name: tests\nrequired: true\n"),
	})
}

// TestGateBlocksMerge (S-10): an agent declares its work complete by opening
// an Engineering Run whose change fails the required tests gate. A person
// other than the author approves the review, and the merge is still refused —
// through work-reviews' real gates client, over gRPC, by the real controller
// running `go test` on the change. Once the agent fixes the change the same
// merge goes through, which proves the refusal was the gate and not the
// stack.
func TestGateBlocksMerge(t *testing.T) {
	s := newPlatformStack(t)
	seedCalc(s)
	mainBefore := s.head("main")

	s.commit("agents/NF-1/work", "main", map[string]*string{
		"calc.go": str("package calc\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int { return a - b }\n"),
	})
	run := s.openAgentRun("agents/NF-1/work")
	s.approveReview(s.member, run.GetId())

	_, err := s.merge(s.member, run.GetId())
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("merge of a change failing its required gate: err = %v, want FailedPrecondition", err)
	}
	if !strings.Contains(err.Error(), `gate "tests" status "fail"`) {
		t.Fatalf("refusal does not name the failing gate: %v", err)
	}
	if got := s.head("main"); got != mainBefore {
		t.Fatalf("main moved from %s to %s although the merge was refused", mainBefore, got)
	}
	if p := s.proof(s.member, run.GetId())["tests"]; p == nil || p.GetStatus() != "fail" {
		t.Fatalf("the run's proof does not record the failing tests gate: %+v", p)
	}

	// The agent fixes the change; the new head is evaluated afresh.
	s.commit("agents/NF-1/work", "main", map[string]*string{"calc.go": str(addGo + "\n// fixed\n")})
	if _, err := s.merge(s.member, run.GetId()); err != nil {
		t.Fatalf("merge after the change passes its gate: %v", err)
	}
	if got := s.head("main"); got == mainBefore {
		t.Fatal("merge reported success but main did not move")
	}
}
