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
