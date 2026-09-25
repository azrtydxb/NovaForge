package gates_test

import "testing"

// This fixture changes only an approval requirement, not executable policy.
// It qualifies real proof-owner authentication without claiming sandbox execution.
func TestApprovalProofUsesGateServiceAuthority(t *testing.T) {
	s := newPlatformStack(t)
	s.commit("main", "", map[string]*string{"README.md": str("proof fixture\n")})
	s.commit("optional-gate", "main", map[string]*string{
		".novaforge/gates/tests.yaml": str("name: tests\nrequired: false\n"),
	})
	run := s.openRun(s.admin, "optional-gate")
	s.approveReview(s.member, run.GetId())
	_, err := s.merge(s.member, run.GetId())
	wantRefused(t, err, "change_gate_config")
	request := s.pendingFor(s.owner, run.GetId(), "change_gate_config")
	if err := s.decide(s.owner, request.GetId(), "approved", "independent approval"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.merge(s.member, run.GetId()); err != nil {
		t.Fatal(err)
	}
	proof := s.proof(s.member, run.GetId())["approval/change_gate_config"]
	if proof == nil || proof.GetStatus() != "pass" || proof.GetProducer() != "gates" {
		t.Fatalf("approval lacks authenticated gates evidence: %+v", proof)
	}
}
