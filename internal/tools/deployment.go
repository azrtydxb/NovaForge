package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/deployment"
)

// DeploymentClient is implemented by the gates/deployment RPC adapter. Repo and
// run bindings come from Runtime, never the model. Authorization and approvals
// are rechecked by the service; the tool does not treat an approval as execution.
type DeploymentClient interface {
	Request(context.Context, deployment.Request) (deployment.Operation, error)
	Get(context.Context, uuid.UUID) (deployment.Operation, error)
	Execute(context.Context, uuid.UUID) (deployment.Operation, error)
	Retry(context.Context, uuid.UUID) (deployment.Operation, error)
	Reconcile(context.Context, uuid.UUID) (deployment.Operation, error)
}

// RegisterDeploymentTools is called from registry composition, before Restrict.
// Register even with a nil client so a missing deployment service is explicitly
// unavailable rather than falsely reported as a successful approval/action.
func RegisterDeploymentTools(r *Registry, client DeploymentClient, repoID uuid.UUID) {
	r.registerExternal("deployment.request", Spec{Description: "Request human approval to deploy an immutable sha256 artifact to an administrator-configured target for this run. This does not execute a deployment. Reuse the same id only for identical intent.", Schema: obj(`"id":{"type":"string","format":"uuid"},"target":{"type":"string"},"artifact":{"type":"string","pattern":"^sha256:[a-f0-9]{64}$"}`, `"id","target","artifact"`)}, func(ctx context.Context, rt Runtime, raw []byte) ([]byte, error) {
		var args struct {
			ID       uuid.UUID `json:"id"`
			Target   string    `json:"target"`
			Artifact string    `json:"artifact"`
		}
		if err := deploymentArgs(raw, &args); err != nil {
			return nil, err
		}
		if client == nil {
			return nil, errors.New("deployment actions are not available in this deployment")
		}
		if repoID == uuid.Nil || rt.RunID == uuid.Nil {
			return nil, errors.New("deployment tool has no bound repository and run")
		}
		op, err := client.Request(ctx, deployment.Request{ID: args.ID, RunID: rt.RunID, RepoID: repoID, Target: args.Target, Artifact: args.Artifact})
		if err != nil {
			return nil, err
		}
		return json.Marshal(op)
	})
	descriptions := map[string]string{
		"deployment.execute":   "Execute this run's approved deployment and return durable attempt evidence. Pending or denied approvals never execute.",
		"deployment.retry":     "Explicitly retry a definitively failed deployment. An unknown outcome must be reconciled instead of blindly resubmitted.",
		"deployment.reconcile": "Read the delivery system to recover an unknown deployment outcome; never resubmit the upgrade.",
		"deployment.status":    "Read this run's deployment intent, approval-bound action and execution attempt evidence.",
	}
	for name, description := range descriptions {
		r.registerExternal(name, Spec{Description: description, Schema: obj(`"id":{"type":"string","format":"uuid"}`, `"id"`)}, func(ctx context.Context, rt Runtime, raw []byte) ([]byte, error) {
			var args struct {
				ID uuid.UUID `json:"id"`
			}
			if err := deploymentArgs(raw, &args); err != nil {
				return nil, err
			}
			if client == nil {
				return nil, errors.New("deployment actions are not available in this deployment")
			}
			if args.ID == uuid.Nil {
				return nil, errors.New("deployment operation id is required")
			}
			op, err := client.Get(ctx, args.ID)
			if err != nil {
				return nil, err
			}
			if op.RunID != rt.RunID || op.RepoID != repoID || rt.RunID == uuid.Nil || repoID == uuid.Nil {
				return nil, errors.New("deployment operation does not belong to this run and repository")
			}
			switch name {
			case "deployment.execute":
				op, err = client.Execute(ctx, args.ID)
			case "deployment.retry":
				op, err = client.Retry(ctx, args.ID)
			case "deployment.reconcile":
				op, err = client.Reconcile(ctx, args.ID)
			}
			if err != nil {
				return nil, err
			}
			return json.Marshal(op)
		})
	}
}

func deploymentArgs(raw []byte, args any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("deployment arguments must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(args); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("deployment arguments must contain one JSON object")
	}
	return nil
}
