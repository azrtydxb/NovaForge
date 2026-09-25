package gates_test

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Project requirements must reach the actual merge admission path, not just
// parse into an otherwise unused configuration field.
func TestProjectRequiredGateBlocksMergeAtTargetRevision(t *testing.T) {
	for _, optionalDefinition := range []bool{false, true} {
		name := "without-definition"
		if optionalDefinition {
			name = "overrides-optional-definition"
		}
		t.Run(name, func(t *testing.T) {
			s := newPlatformStack(t)
			files := map[string]*string{
				"go.mod": str(goMod), "calc.go": str(addGo), "calc_test.go": str(addTest),
				".novaforge/project.yaml": str("gates: [tests]\n"),
			}
			if optionalDefinition {
				files[".novaforge/gates/tests.yaml"] = str("name: tests\nrequired: false\n")
			}
			s.commit("main", "", files)
			before := s.head("main")
			s.commit("agents/project-policy/work", "main", map[string]*string{
				"calc.go":                 str("package calc\n\nfunc Add(a, b int) int { return a - b }\n"),
				".novaforge/project.yaml": str("gates: []\n"),
			})
			run := s.openAgentRun("agents/project-policy/work")
			s.approveReview(s.member, run.GetId())
			_, err := s.merge(s.member, run.GetId())
			if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), `gate "tests" status "fail"`) {
				t.Fatalf("target project requirement did not refuse failing source: %v", err)
			}
			if got := s.head("main"); got != before {
				t.Fatal("required project gate failure moved the target ref")
			}
		})
	}
}
