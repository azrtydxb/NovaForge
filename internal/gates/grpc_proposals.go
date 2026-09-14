package gates

import (
	"context"
	"encoding/json"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
)

func (g *GRPCServer) proposer() (*Proposer, error) {
	if g.Proposals == nil {
		return nil, status.Error(codes.Unimplemented, "this deployment has no gate configuration path wired")
	}
	return g.Proposals, nil
}

// ListGateConfig reports every known gate as the repository's default branch
// declares it.
func (g *GRPCServer) ListGateConfig(ctx context.Context, req *gatesv1.ListGateConfigRequest) (*gatesv1.ListGateConfigResponse, error) {
	p, err := g.proposer()
	if err != nil {
		return nil, err
	}
	ref, sha, states, err := p.List(ctx, req.GetRepo())
	if err != nil {
		return nil, err
	}
	out := make([]*gatesv1.GateConfig, 0, len(states))
	for _, s := range states {
		params := s.Params
		if params == nil {
			params = map[string]any{}
		}
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "gate %q params are not representable as JSON: %v", s.Name, err)
		}
		out = append(out, &gatesv1.GateConfig{
			Name: s.Name, Declared: s.Declared, Enabled: s.Enabled, Path: s.Path, ParamsJson: string(raw),
		})
	}
	return &gatesv1.ListGateConfigResponse{Ref: ref, CommitSha: sha, Gates: out}, nil
}

// ProposeGateChange commits a gate change to a new branch and opens it as an
// Engineering Run. It never writes to the default branch.
func (g *GRPCServer) ProposeGateChange(ctx context.Context, req *gatesv1.ProposeGateChangeRequest) (*gatesv1.ProposeGateChangeResponse, error) {
	p, err := g.proposer()
	if err != nil {
		return nil, err
	}
	change := GateChange{Repo: req.GetRepo(), Gate: req.GetGate()}
	if req.Enabled != nil {
		v := req.GetEnabled()
		change.Enabled = &v
	}
	if req.GetParamsJson() != "" {
		var params map[string]any
		if err := json.Unmarshal([]byte(req.GetParamsJson()), &params); err != nil || params == nil {
			return nil, status.Errorf(codes.InvalidArgument, "params_json must be a JSON object: %v", err)
		}
		change.Params = params
	}
	prop, err := p.Propose(ctx, change)
	if err != nil {
		return nil, err
	}
	return &gatesv1.ProposeGateChangeResponse{
		RunId: prop.RunID, RunNumber: prop.RunNumber, Branch: prop.Branch, CommitSha: prop.CommitSHA, Title: prop.Title,
	}, nil
}
