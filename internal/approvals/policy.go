// Package approvals owns the approvals schema and the approval policy: the
// model never decides its own permissions, so Decide takes only a policy and
// a capability grant — never anything sourced from model output.
package approvals

import (
	"context"

	"github.com/novaforge/novaforge/internal/capability"
)

// Action is the fixed set of actions an agent or human contributor can take
// that the platform gates behind an approval decision. It is a closed enum:
// Decide's exhaustive switch has a default that forbids anything outside it,
// so a new action added elsewhere in the codebase is denied until somebody
// deliberately writes its rule here.
type Action string

const (
	ActionReadSource       Action = "read_source"
	ActionModifyWorkspace  Action = "modify_workspace"
	ActionAddDependency    Action = "add_dependency"
	ActionChangeDBSchema   Action = "change_db_schema"
	ActionAccessSecret     Action = "access_secret"
	ActionDeployStaging    Action = "deploy_staging"
	ActionDeployProduction Action = "deploy_production"
	// ActionChangeGateConfig is a change to .novaforge/gates: the definitions
	// that judge every later change. Gates are read from the target branch, so
	// a change cannot weaken the gates judging itself — but once merged it
	// would weaken them for everything after it, which is why it needs a
	// person with authority over the repository.
	ActionChangeGateConfig Action = "change_gate_config"
)

// Decision is the outcome of evaluating an Action against a Policy and a
// capability.Grant.
type Decision string

const (
	// DecisionAutomatic proceeds with no approval step at all.
	DecisionAutomatic Decision = "automatic"
	// DecisionPolicy proceeds automatically but only because policy already
	// encodes the rule under which it is allowed (as opposed to being
	// inherently safe like DecisionAutomatic).
	DecisionPolicy Decision = "policy"
	// DecisionHuman requires a human to approve before the action proceeds.
	DecisionHuman Decision = "human"
	// DecisionForbidden refuses the action outright regardless of any
	// approval: the grant does not permit it, or the action is unrecognized.
	DecisionForbidden Decision = "forbidden"
)

// Policy holds an organization's approval configuration. It is deliberately
// the only source of approval rules Decide consults beyond the grant: never
// a request body, never model output.
type Policy struct {
	OrgID string
}

// Decide evaluates a as an exhaustive switch over the fixed Action enum.
// Its signature carries only a Policy and a capability.Grant — nothing
// sourced from model output — so the model can never widen or alter what it
// is approved to do. The switch's default returns DecisionForbidden, so an
// action added to the codebase without a corresponding case here is denied
// rather than silently permitted.
func Decide(ctx context.Context, p Policy, a Action, g capability.Grant) (Decision, error) {
	switch a {
	case ActionReadSource, ActionModifyWorkspace:
		// Reading source and writing within the agent's already-scoped
		// workspace branch require no separate approval step: capability
		// grants already constrain what can be read or written.
		return DecisionAutomatic, nil

	case ActionAddDependency:
		// Adding a dependency is allowed under policy (the dependencies
		// gate and a human reviewer still see it in the diff) without a
		// separate blocking approval request.
		return DecisionPolicy, nil

	case ActionChangeDBSchema:
		// Schema changes always require a human with architecture approval
		// authority: this is never automatic and never merely policy-gated.
		return DecisionHuman, nil

	case ActionAccessSecret:
		// Secret access is broker-mediated (see internal/secrets) and
		// scoped by the grant already; the broker itself enforces
		// environment scoping, so policy governs whether a request may be
		// made at all.
		return DecisionPolicy, nil

	case ActionChangeGateConfig:
		// Deleting or weakening a gate is the one change that makes every
		// later refusal softer, so it is never automatic and never policy.
		return DecisionHuman, nil

	case ActionDeployStaging:
		if !g.DeployStaging {
			return DecisionForbidden, nil
		}
		return DecisionHuman, nil

	case ActionDeployProduction:
		if !g.DeployProd {
			return DecisionForbidden, nil
		}
		return DecisionHuman, nil

	default:
		return DecisionForbidden, nil
	}
}

// Rule is one row of the approval policy: an action, and what the platform
// requires before it proceeds. Actions whose decision depends on the caller's
// grant say so, because "human" and "forbidden" are the same action seen
// through two different grants.
type Rule struct {
	Action   Action
	Decision Decision
	// GrantDependent reports that the decision above is what an agent with
	// the corresponding capability gets, and that an agent without it is
	// refused outright.
	GrantDependent bool
}

// Rules returns the whole policy, in the order Decide's switch reads. It
// exists so a person can be shown what governs agents here without anyone
// writing a second copy of the table that then drifts from the switch.
func Rules() []Rule {
	return []Rule{
		{ActionReadSource, DecisionAutomatic, false},
		{ActionModifyWorkspace, DecisionAutomatic, false},
		{ActionAddDependency, DecisionPolicy, false},
		{ActionChangeDBSchema, DecisionHuman, false},
		{ActionAccessSecret, DecisionPolicy, false},
		{ActionChangeGateConfig, DecisionHuman, false},
		{ActionDeployStaging, DecisionHuman, true},
		{ActionDeployProduction, DecisionHuman, true},
	}
}
