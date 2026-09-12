package approvals_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/novaforge/novaforge/internal/approvals"
	"github.com/novaforge/novaforge/internal/capability"
)

func TestReadSourceIsAutomatic(t *testing.T) {
	d, err := approvals.Decide(context.Background(), approvals.Policy{}, approvals.ActionReadSource, capability.Grant{})
	if err != nil {
		t.Fatalf("Decide(read_source): %v", err)
	}
	if d != approvals.DecisionAutomatic {
		t.Fatalf("want automatic, got %q", d)
	}

	d, err = approvals.Decide(context.Background(), approvals.Policy{}, approvals.ActionModifyWorkspace, capability.Grant{})
	if err != nil {
		t.Fatalf("Decide(modify_workspace): %v", err)
	}
	if d != approvals.DecisionAutomatic {
		t.Fatalf("want automatic, got %q", d)
	}
}

func TestChangeDBSchemaRequiresArchitectureApproval(t *testing.T) {
	d, err := approvals.Decide(context.Background(), approvals.Policy{}, approvals.ActionChangeDBSchema, capability.Grant{})
	if err != nil {
		t.Fatalf("Decide(change_db_schema): %v", err)
	}
	if d != approvals.DecisionHuman {
		t.Fatalf("want human, got %q", d)
	}
}

func TestDeployProductionForbiddenWithoutGrant(t *testing.T) {
	d, err := approvals.Decide(context.Background(), approvals.Policy{}, approvals.ActionDeployProduction, capability.Grant{DeployProd: false})
	if err != nil {
		t.Fatalf("Decide(deploy_production, no grant): %v", err)
	}
	if d != approvals.DecisionForbidden {
		t.Fatalf("want forbidden without grant, got %q", d)
	}

	d, err = approvals.Decide(context.Background(), approvals.Policy{}, approvals.ActionDeployProduction, capability.Grant{DeployProd: true})
	if err != nil {
		t.Fatalf("Decide(deploy_production, granted): %v", err)
	}
	if d != approvals.DecisionHuman {
		t.Fatalf("want human when the grant allows it, got %q", d)
	}
}

func TestUnknownActionIsForbidden(t *testing.T) {
	d, err := approvals.Decide(context.Background(), approvals.Policy{}, approvals.Action("delete_organization"), capability.Grant{})
	if err != nil {
		t.Fatalf("Decide(unknown action): %v", err)
	}
	if d != approvals.DecisionForbidden {
		t.Fatalf("want forbidden for an action outside the enum (deny by default), got %q", d)
	}
}

// TestPolicyIsNotDerivedFromModelOutput asserts Decide's signature carries
// only a context, a Policy, an Action, and a capability.Grant — nothing that
// could be sourced from a model's own output (e.g. free-form text, a
// message, or a "reason" string an agent supplies). This is what makes it
// structurally impossible for the model to decide its own permissions: it
// simply has no parameter through which to say anything.
func TestPolicyIsNotDerivedFromModelOutput(t *testing.T) {
	fn := reflect.TypeOf(approvals.Decide)
	if fn.Kind() != reflect.Func {
		t.Fatal("approvals.Decide is not a function")
	}
	if fn.NumIn() != 4 {
		t.Fatalf("want exactly 4 parameters (ctx, Policy, Action, Grant), got %d", fn.NumIn())
	}

	wantTypes := []reflect.Type{
		reflect.TypeOf((*context.Context)(nil)).Elem(),
		reflect.TypeOf(approvals.Policy{}),
		reflect.TypeOf(approvals.Action("")),
		reflect.TypeOf(capability.Grant{}),
	}
	for i, want := range wantTypes {
		got := fn.In(i)
		if got != want {
			t.Fatalf("parameter %d: want %v, got %v", i, want, got)
		}
	}
}

// TestRulesMatchDecide pins the two against each other. Rules() exists so a
// person can be shown the policy; if it could drift from Decide's switch it
// would be showing them something the platform does not actually enforce,
// which is worse than showing nothing.
func TestRulesMatchDecide(t *testing.T) {
	// A grant with every deploy capability, so the grant-dependent actions
	// report the decision they reach rather than the refusal they get
	// without it.
	full := capability.Grant{DeployStaging: true, DeployProd: true}

	for _, r := range approvals.Rules() {
		got, err := approvals.Decide(context.Background(), approvals.Policy{}, r.Action, full)
		if err != nil {
			t.Fatalf("Decide(%s): %v", r.Action, err)
		}
		if got != r.Decision {
			t.Errorf("Rules() says %s is %q, Decide says %q", r.Action, r.Decision, got)
		}

		// A grant-dependent rule must actually depend on the grant.
		without, err := approvals.Decide(context.Background(), approvals.Policy{}, r.Action, capability.Grant{})
		if err != nil {
			t.Fatalf("Decide(%s) without grant: %v", r.Action, err)
		}
		if r.GrantDependent && without != approvals.DecisionForbidden {
			t.Errorf("%s is marked grant-dependent but is %q without the grant", r.Action, without)
		}
		if !r.GrantDependent && without != r.Decision {
			t.Errorf("%s is not marked grant-dependent but changes to %q without the grant", r.Action, without)
		}
	}
}

// TestRulesCoverEveryAction pins that no action is missing from the shown
// policy: an action Decide knows about but Rules() omits is a rule the user
// is never told about.
func TestRulesCoverEveryAction(t *testing.T) {
	shown := map[approvals.Action]bool{}
	for _, r := range approvals.Rules() {
		shown[r.Action] = true
	}
	for _, a := range []approvals.Action{
		approvals.ActionReadSource,
		approvals.ActionModifyWorkspace,
		approvals.ActionAddDependency,
		approvals.ActionChangeDBSchema,
		approvals.ActionAccessSecret,
		approvals.ActionDeployStaging,
		approvals.ActionDeployProduction,
	} {
		if !shown[a] {
			t.Errorf("action %q is enforced but never shown by Rules()", a)
		}
	}
}
