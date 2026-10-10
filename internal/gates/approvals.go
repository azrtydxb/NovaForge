package gates

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/approvals"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/work"
)

// approvalProofPrefix names an approval decision in a run's proof, next to its
// gates, so a reader sees what was decided about the change and by whom.
const approvalProofPrefix = "approval/"

// dependenciesGate is the gate a dependency addition is held to under policy.
const dependenciesGate = "dependencies"

// changeRequirements classifies what the run's change does. It returns nothing
// when the controller has no approvals store, which only a unit test builds:
// NewGRPCServer wires the store the service was given, so a deployed
// controller always classifies.
func (c *Controller) changeRequirements(ctx context.Context, head RunHead) ([]Requirement, error) {
	if c.Approvals == nil || head.SourceRef == "" {
		return nil, nil
	}
	target, source := head.TargetRef, head.SourceRef
	if head.TargetSHA != "" {
		target = head.TargetSHA
		source = head.HeadSHA
	}
	reqs, err := ClassifyChange(ctx, c.Git, head.RepoID, target, source, head.sourceRepo())
	if err != nil {
		return nil, fmt.Errorf("classify change: %w", err)
	}
	return reqs, nil
}

// requireDependencyGate makes the dependencies gate required when the change
// adds a dependency. That is what "policy controlled" means for this action:
// no person is asked, and the change merges only if the dependency it adds has
// no known advisory — decided by a tool against a database, not by a model.
func requireDependencyGate(defs []Definition, reqs []Requirement) []Definition {
	for _, r := range reqs {
		if r.Action != approvals.ActionAddDependency {
			continue
		}
		for i := range defs {
			if defs[i].Name == dependenciesGate {
				defs[i].Required = true
				return defs
			}
		}
		return append(defs, Definition{Name: dependenciesGate, Required: true})
	}
	return defs
}

// approvalReasons decides each requirement against the approval policy and
// returns why the run may not merge yet, raising a request for a person where
// the policy asks for one. When record is set each outcome is also written as
// the run's proof.
//
// The decision's inputs are the policy, the action found in the diff, and a
// grant naming the author with no capabilities. A run's author holds no grant
// for these actions — they are decided at merge, not granted — and nothing the
// run says about itself reaches approvals.Decide.
func (c *Controller) approvalReasons(ctx context.Context, runID uuid.UUID, head RunHead, reqs []Requirement, record bool) ([]string, error) {
	var reasons []string
	for _, r := range reqs {
		grant := capability.Grant{OrgID: head.OrgID, SubjectID: head.AuthorID, SubjectKind: head.AuthorKind}
		decision, err := approvals.Decide(ctx, approvals.Policy{OrgID: head.OrgID.String()}, r.Action, grant)
		if err != nil {
			return nil, fmt.Errorf("decide %s: %w", r.Action, err)
		}
		switch decision {
		case approvals.DecisionAutomatic:
			continue

		case approvals.DecisionPolicy:
			if record {
				detail := fmt.Sprintf("%s (%s); allowed by policy, provided the %s gate passes",
					r.Reason, strings.Join(r.Paths, ", "), dependenciesGate)
				if err := c.proof(ctx, runID, approvalProofPrefix+string(r.Action), "pass", detail); err != nil {
					return nil, err
				}
			}

		case approvals.DecisionHuman:
			req, err := c.Approvals.Raise(ctx, approvals.ApprovalRequest{
				OrgID: head.OrgID, RunID: runID, Action: r.Action,
				HeadSHA: head.HeadSHA, Reason: r.Reason, Paths: r.Paths,
				AuthorID: head.AuthorID, AuthorKind: head.AuthorKind,
			})
			if err != nil {
				return nil, fmt.Errorf("raise %s approval: %w", r.Action, err)
			}
			switch {
			case req.Decision == approvals.StateApproved && req.DecidedBy != head.AuthorID:
				// Approved for this head by someone other than the author.
			case req.Decision == approvals.StateDenied:
				reasons = append(reasons, fmt.Sprintf("approval %q denied for head %s: %s", r.Action, head.HeadSHA, req.Comment))
			default:
				reasons = append(reasons, fmt.Sprintf("approval %q awaiting an owner or admin for head %s: %s", r.Action, head.HeadSHA, r.Reason))
				c.notifyInbox(ctx, head, runID, req, r)
			}
			if record {
				if err := c.recordApprovalProof(ctx, req); err != nil {
					return nil, err
				}
			}

		default:
			reasons = append(reasons, fmt.Sprintf("action %q is forbidden by policy: %s", r.Action, r.Reason))
			if record {
				if err := c.proof(ctx, runID, approvalProofPrefix+string(r.Action), "fail", "forbidden by policy: "+r.Reason); err != nil {
					return nil, err
				}
			}
		}
	}
	return reasons, nil
}

// recordApprovalProof writes one human approval's state as the run's proof.
func (c *Controller) recordApprovalProof(ctx context.Context, req approvals.ApprovalRequest) error {
	status, detail := "pending", fmt.Sprintf("%s (%s); awaiting an owner or admin for head %s",
		req.Reason, strings.Join(req.Paths, ", "), req.HeadSHA)
	switch req.Decision {
	case approvals.StateApproved:
		status = "pass"
		detail = fmt.Sprintf("%s (%s); approved by %s for head %s", req.Reason, strings.Join(req.Paths, ", "), req.DecidedBy, req.HeadSHA)
	case approvals.StateDenied:
		status = "fail"
		detail = fmt.Sprintf("%s (%s); denied by %s for head %s", req.Reason, strings.Join(req.Paths, ", "), req.DecidedBy, req.HeadSHA)
	}
	if req.Comment != "" {
		detail += ": " + req.Comment
	}
	return c.proof(ctx, req.RunID, approvalProofPrefix+string(req.Action), status, detail)
}

func (c *Controller) proof(ctx context.Context, runID uuid.UUID, gate, status, detail string) error {
	if c.Proof == nil {
		return nil
	}
	if err := c.Proof(ctx, runID, gate, status, detail); err != nil {
		return fmt.Errorf("record proof for %q: %w", gate, err)
	}
	return nil
}

// notifyInbox publishes an approval request to the run's author, so the
// person whose change is now held waiting for an owner or admin hears about
// it the moment the gates raised it. Publishing goes through the work-reviews
// service's ingest RPC rather than across the schema boundary. An approval
// addressed to the person who can decide it would be better still — but no
// RPC on identity names an organization's approvers to a service caller
// (ListOrgMembers needs a member's own credential), so the author is who this
// publisher can honestly address, and nobody is invented.
func (c *Controller) notifyInbox(ctx context.Context, head RunHead, runID uuid.UUID, req approvals.ApprovalRequest, r Requirement) {
	if c.Inbox == nil {
		return
	}
	if head.AuthorKind != "user" || head.AuthorID == uuid.Nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := c.Inbox.PublishInboxItem(ctx, &workv1.PublishInboxItemRequest{
		OrgId: head.OrgID.String(), RepoId: head.RepoID.String(),
		Reason:  work.ReasonApproval,
		Ref:     c.runRef(ctx, runID, head),
		Title:   fmt.Sprintf("%s: awaiting an owner or admin", r.Action),
		Body:    fmt.Sprintf("%s (%s)", r.Reason, strings.Join(r.Paths, ", ")),
		ActorId: head.AuthorID.String(), ActorKind: head.AuthorKind,
		DedupeKey:  "approval:" + req.ID.String(),
		Recipients: []string{head.AuthorID.String()},
	}); err != nil {
		log.Printf("gates: publish approval notification: %v", err)
	}
}

// runRef renders a run for a notification ref: its number when the reviews
// service names it, the evaluated head otherwise — a ref the reader cannot
// connect to the change is worse than the change's own sha.
func (c *Controller) runRef(ctx context.Context, runID uuid.UUID, head RunHead) string {
	if c.RunNumber != nil {
		if n, err := c.RunNumber(ctx, runID); err == nil && n > 0 {
			return fmt.Sprintf("run #%d", n)
		}
	}
	if len(head.HeadSHA) >= 8 {
		return "run " + head.HeadSHA[:8]
	}
	return "run"
}
