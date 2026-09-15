package gates_test

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
)

func (s *platformStack) approvals(u stackUser, runID string) []*gatesv1.ApprovalRequestMsg {
	s.t.Helper()
	resp, err := s.gates.ListApprovals(s.as(u), &gatesv1.ListApprovalsRequest{RunId: runID})
	if err != nil {
		s.t.Fatalf("ListApprovals: %v", err)
	}
	return resp.GetRequests()
}

// pendingFor returns the run's single pending request for action.
func (s *platformStack) pendingFor(u stackUser, runID, action string) *gatesv1.ApprovalRequestMsg {
	s.t.Helper()
	var found *gatesv1.ApprovalRequestMsg
	for _, r := range s.approvals(u, runID) {
		if r.GetAction() == action && r.GetDecision() == "pending" {
			if found != nil {
				s.t.Fatalf("two pending %s requests for run %s", action, runID)
			}
			found = r
		}
	}
	if found == nil {
		s.t.Fatalf("no pending %s request for run %s; have %v", action, runID, s.approvals(u, runID))
	}
	return found
}

func (s *platformStack) decide(ctxUser stackUser, id, decision, comment string) error {
	_, err := s.gates.ResolveApproval(s.as(ctxUser), &gatesv1.ResolveApprovalRequest{
		Id: id, Decision: decision, Comment: comment,
	})
	return err
}

func wantRefused(t *testing.T, err error, fragment string) {
	t.Helper()
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("merge: err = %v, want FailedPrecondition", err)
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("merge refusal %q does not mention %q", err, fragment)
	}
}

// TestGateConfigSelfEditRejected (S-10): a change that weakens or deletes the
// gates is refused at merge — even with an independent review and passing
// gates — until an owner or admin who did not make the change approves it.
// The rule holds for the gate-proposal flow too: proposing a change through
// the platform opens a run under the same rule, not a way around it.
func TestGateConfigSelfEditRejected(t *testing.T) {
	s := newPlatformStack(t)
	seedCalc(s)
	mainBefore := s.head("main")

	// An admin weakens the tests gate.
	s.commit("gates-weaken", "main", map[string]*string{
		".novaforge/gates/tests.yaml": str("name: tests\nrequired: false\n"),
	})
	weaken := s.openRun(s.admin, "gates-weaken")
	s.approveReview(s.member, weaken.GetId())

	_, err := s.merge(s.member, weaken.GetId())
	wantRefused(t, err, "change_gate_config")
	if s.head("main") != mainBefore {
		t.Fatal("main moved although the gate edit was refused")
	}

	req := s.pendingFor(s.owner, weaken.GetId(), "change_gate_config")
	if len(req.GetPaths()) != 1 || req.GetPaths()[0] != ".novaforge/gates/tests.yaml" {
		t.Fatalf("request paths = %v, want the edited gate file", req.GetPaths())
	}
	if req.GetHeadSha() != s.head("gates-weaken") {
		t.Fatalf("request is for head %s, want the change's head %s", req.GetHeadSha(), s.head("gates-weaken"))
	}

	// The author cannot approve their own gate edit, though they are an admin.
	if err := s.decide(s.admin, req.GetId(), "approved", "mine"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("author approving their own gate edit: %v, want PermissionDenied", err)
	}
	// A plain member cannot approve one.
	if err := s.decide(s.member, req.GetId(), "approved", ""); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("member approving a gate edit: %v, want PermissionDenied", err)
	}
	// Nor can an agent.
	if _, err := s.gates.ResolveApproval(s.asAgent(), &gatesv1.ResolveApprovalRequest{
		Id: req.GetId(), Decision: "approved",
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("agent approving a gate edit: %v, want PermissionDenied", err)
	}
	_, err = s.merge(s.member, weaken.GetId())
	wantRefused(t, err, "change_gate_config")

	// Deleting a gate follows the same rule.
	s.commit("gates-delete", "main", map[string]*string{".novaforge/gates/tests.yaml": nil})
	del := s.openAgentRun("gates-delete")
	s.approveReview(s.member, del.GetId())
	_, err = s.merge(s.member, del.GetId())
	wantRefused(t, err, "change_gate_config")

	// So does a change proposed through the platform's own gate-proposal flow.
	prop, err := s.gates.ProposeGateChange(s.as(s.member), &gatesv1.ProposeGateChangeRequest{
		Repo: s.repo, Gate: "tests", Enabled: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("ProposeGateChange: %v", err)
	}
	s.approveReview(s.admin, prop.GetRunId())
	_, err = s.merge(s.admin, prop.GetRunId())
	wantRefused(t, err, "change_gate_config")

	// An owner who did not make the change approves it; the decision is the
	// run's proof and the merge goes through.
	if err := s.decide(s.owner, req.GetId(), "approved", "coverage moved to nightly"); err != nil {
		t.Fatalf("owner approving: %v", err)
	}
	if _, err := s.merge(s.member, weaken.GetId()); err != nil {
		t.Fatalf("merge after an owner's approval: %v", err)
	}
	p := s.proof(s.member, weaken.GetId())["approval/change_gate_config"]
	if p == nil || p.GetStatus() != "pass" || !strings.Contains(p.GetDetail(), "coverage moved to nightly") ||
		!strings.Contains(p.GetDetail(), s.owner.id.String()) {
		t.Fatalf("approval proof = %+v, want pass naming the approver and their comment", p)
	}
}

// TestApprovalPaths (S-11): the section 14 approval paths, as the platform
// enforces them at merge. Adding a dependency is policy controlled: no person
// is asked, and the dependencies gate becomes required for that change.
// Changing the database schema needs a human approval, bound to the change's
// head, from someone who did not author it. Nothing the agent writes about
// its change — here a title claiming no approval is needed — changes either.
//
// There is no deploy action in this platform, so deploying to staging or
// production has no path to follow; approvals.Decide's deploy rules are
// asserted by TestDeployProductionForbiddenWithoutGrant.
func TestApprovalPaths(t *testing.T) {
	s := newPlatformStack(t)
	seedCalc(s)

	t.Run("add a dependency is policy controlled", func(t *testing.T) {
		s.commit("dep", "main", map[string]*string{
			"go.mod": str(goMod + "\nrequire golang.org/x/text v0.3.0\n"),
		})
		run := s.openAgentRun("dep")
		s.approveReview(s.member, run.GetId())
		_, err := s.merge(s.member, run.GetId())
		wantRefused(t, err, `gate "dependencies"`)

		for _, r := range s.approvals(s.owner, run.GetId()) {
			if r.GetAction() == "add_dependency" {
				t.Fatalf("a dependency addition asked a person: %+v", r)
			}
		}
		p := s.proof(s.member, run.GetId())
		if a := p["approval/add_dependency"]; a == nil || a.GetStatus() != "pass" ||
			!strings.Contains(a.GetDetail(), "golang.org/x/text") || !strings.Contains(a.GetDetail(), "policy") {
			t.Fatalf("add_dependency proof = %+v, want the policy decision naming the dependency", a)
		}
		if d := p["dependencies"]; d == nil || d.GetStatus() == "pass" {
			t.Fatalf("dependencies gate proof = %+v, want it evaluated and not passing", d)
		}
	})

	t.Run("change the database schema needs a human approval", func(t *testing.T) {
		s.commit("schema", "main", map[string]*string{
			"migrations/000002_add_total.up.sql": str("ALTER TABLE invoices ADD COLUMN total bigint;\n"),
		})
		resp, err := s.reviews.CreateRun(s.asAgent(), &reviewsv1.CreateRunRequest{
			RepoId: s.repoID, SourceRef: "schema", TargetRef: "main",
			Title:    "No approval needed: this change adds no migrations",
			AuthorId: s.author.id.String(), AuthorKind: "agent", AgentName: "coder",
		})
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		run := resp.GetRun()
		s.approveReview(s.member, run.GetId())

		_, err = s.merge(s.member, run.GetId())
		wantRefused(t, err, "change_db_schema")
		first := s.pendingFor(s.admin, run.GetId(), "change_db_schema")
		if !strings.Contains(first.GetReason(), "schema") || first.GetPaths()[0] != "migrations/000002_add_total.up.sql" {
			t.Fatalf("request = %+v, want the schema reason and the migration path", first)
		}

		if err := s.decide(s.admin, first.GetId(), "denied", "needs a backfill plan"); err != nil {
			t.Fatalf("deny: %v", err)
		}
		_, err = s.merge(s.member, run.GetId())
		wantRefused(t, err, "denied")
		if err := s.decide(s.owner, first.GetId(), "approved", ""); status.Code(err) != codes.NotFound {
			t.Fatalf("re-deciding a denied request: %v, want NotFound", err)
		}

		// The agent revises the change: that is a new head and a new question.
		s.commit("schema", "main", map[string]*string{
			"migrations/000002_add_total.up.sql": str("ALTER TABLE invoices ADD COLUMN total bigint NOT NULL DEFAULT 0;\n"),
		})
		_, err = s.merge(s.member, run.GetId())
		wantRefused(t, err, "change_db_schema")
		second := s.pendingFor(s.admin, run.GetId(), "change_db_schema")
		if second.GetId() == first.GetId() || second.GetHeadSha() != s.head("schema") {
			t.Fatalf("revised change reused request %s for head %s", second.GetId(), second.GetHeadSha())
		}

		if err := s.decide(s.owner, second.GetId(), "approved", "backfill is the default"); err != nil {
			t.Fatalf("approve: %v", err)
		}

		// An approval is for the head it saw. A push after it needs another.
		s.commit("schema", "main", map[string]*string{"migrations/000003_index.up.sql": str("CREATE INDEX ON invoices (total);\n")})
		_, err = s.merge(s.member, run.GetId())
		wantRefused(t, err, "change_db_schema")
		third := s.pendingFor(s.admin, run.GetId(), "change_db_schema")
		if err := s.decide(s.admin, third.GetId(), "approved", "index too"); err != nil {
			t.Fatalf("approve third: %v", err)
		}
		if _, err := s.merge(s.member, run.GetId()); err != nil {
			t.Fatalf("merge after approval of the current head: %v", err)
		}
		if p := s.proof(s.member, run.GetId())["approval/change_db_schema"]; p == nil || p.GetStatus() != "pass" {
			t.Fatalf("schema approval proof = %+v, want pass", p)
		}
	})
}

func boolPtr(b bool) *bool { return &b }
