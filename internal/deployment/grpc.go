package deployment

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	deploymentv1 "github.com/novaforge/novaforge/gen/novaforge/deployment/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type GRPCServer struct{ service *Service }

func NewGRPCServer(s *Service) *GRPCServer { return &GRPCServer{service: s} }

func deploymentID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, status.Error(codes.InvalidArgument, "deployment requires a nonzero UUID")
	}
	return id, nil
}
func deploymentError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "deployment request canceled; read durable status")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "deployment request timed out; read durable status")
	case errors.Is(err, pgx.ErrNoRows):
		return status.Error(codes.NotFound, "deployment not found")
	case errors.Is(err, ErrBusy):
		return status.Error(codes.Aborted, ErrBusy.Error())
	case errors.Is(err, ErrConflict), errors.Is(err, ErrApprovalRequired), errors.Is(err, ErrRetryRequired), errors.Is(err, ErrUncertain), errors.Is(err, ErrDeliveryFailed):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return status.Error(codes.PermissionDenied, "deployment request denied or unavailable")
	}
}
func (s *GRPCServer) authorize(ctx context.Context) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return status.Error(codes.Unauthenticated, "deployment requires authentication")
	}
	if scope.OrgID == uuid.Nil || scope.ActorID == uuid.Nil || (scope.ActorKind != "agent" && !(scope.ActorKind == "user" && (scope.Role == "member" || scope.IsOrgAdmin()))) {
		return status.Error(codes.PermissionDenied, "deployment requires organization actor")
	}
	if s.service == nil {
		return status.Error(codes.Unavailable, "deployment is not configured")
	}
	return nil
}
func (s *GRPCServer) RequestDeployment(ctx context.Context, r *deploymentv1.RequestDeploymentRequest) (*deploymentv1.RequestDeploymentResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	id, err := deploymentID(r.GetId())
	if err != nil {
		return nil, err
	}
	run, err := deploymentID(r.GetRunId())
	if err != nil {
		return nil, err
	}
	repo, err := deploymentID(r.GetRepoId())
	if err != nil {
		return nil, err
	}
	if !artifactDigest.MatchString(r.GetArtifact()) || r.GetTarget() == "" {
		return nil, status.Error(codes.InvalidArgument, "deployment requires fixed target and immutable artifact digest")
	}
	op, err := s.service.Request(ctx, Request{ID: id, RunID: run, RepoID: repo, Target: r.GetTarget(), Artifact: r.GetArtifact()})
	if err != nil {
		return nil, deploymentError(err)
	}
	return &deploymentv1.RequestDeploymentResponse{Operation: operationProto(op)}, nil
}
func (s *GRPCServer) GetDeployment(ctx context.Context, r *deploymentv1.GetDeploymentRequest) (*deploymentv1.GetDeploymentResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	id, err := deploymentID(r.GetId())
	if err != nil {
		return nil, err
	}
	op, err := s.service.Get(ctx, id)
	if err != nil {
		return nil, deploymentError(err)
	}
	return &deploymentv1.GetDeploymentResponse{Operation: operationProto(op)}, nil
}
func (s *GRPCServer) ExecuteDeployment(ctx context.Context, r *deploymentv1.ExecuteDeploymentRequest) (*deploymentv1.ExecuteDeploymentResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	id, err := deploymentID(r.GetId())
	if err != nil {
		return nil, err
	}
	op, err := s.service.Execute(ctx, id)
	if err != nil {
		return nil, deploymentError(err)
	}
	return &deploymentv1.ExecuteDeploymentResponse{Operation: operationProto(op)}, nil
}
func (s *GRPCServer) RetryDeployment(ctx context.Context, r *deploymentv1.RetryDeploymentRequest) (*deploymentv1.RetryDeploymentResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	id, err := deploymentID(r.GetId())
	if err != nil {
		return nil, err
	}
	op, err := s.service.Retry(ctx, id)
	if err != nil {
		return nil, deploymentError(err)
	}
	return &deploymentv1.RetryDeploymentResponse{Operation: operationProto(op)}, nil
}
func (s *GRPCServer) ReconcileDeployment(ctx context.Context, r *deploymentv1.ReconcileDeploymentRequest) (*deploymentv1.ReconcileDeploymentResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	id, err := deploymentID(r.GetId())
	if err != nil {
		return nil, err
	}
	op, err := s.service.Reconcile(ctx, id)
	if err != nil {
		return nil, deploymentError(err)
	}
	return &deploymentv1.ReconcileDeploymentResponse{Operation: operationProto(op)}, nil
}
func operationProto(op Operation) *deploymentv1.Operation {
	out := &deploymentv1.Operation{Id: op.ID.String(), RunId: op.RunID.String(), RepoId: op.RepoID.String(), Target: op.Target, Artifact: op.Artifact, OrgId: op.OrgID.String(), ActorId: op.ActorID.String(), ActorKind: op.ActorKind, Environment: op.Environment, TargetRevision: op.TargetRevision, Destination: op.Destination, State: op.State, CreatedAt: op.CreatedAt.Format(time.RFC3339Nano)}
	for _, a := range op.Attempts {
		v := &deploymentv1.Attempt{ActorId: a.ActorID.String(), ActorKind: a.ActorKind, Kind: a.Kind, Number: int32(a.Number), State: a.State, StartedAt: a.StartedAt.Format(time.RFC3339Nano), ExternalId: a.Result.ExternalID, Summary: a.Result.Summary, Error: a.Error}
		if a.FinishedAt != nil {
			v.FinishedAt = a.FinishedAt.Format(time.RFC3339Nano)
		}
		out.Attempts = append(out.Attempts, v)
	}
	for _, c := range op.Credentials {
		v := &deploymentv1.CredentialObligation{Attempt: int32(c.Attempt), ProviderBinding: c.ProviderBinding, Phase: c.Phase}
		if c.ResolvedAt != nil {
			v.ResolvedAt = c.ResolvedAt.Format(time.RFC3339Nano)
		}
		out.Credentials = append(out.Credentials, v)
	}
	return out
}

// Client preserves the service-owned operation shape for tools and REST callers.
// Its connection must forward the original verified caller credential.
type Client struct {
	rpc deploymentv1.DeploymentServiceClient
}

func NewClient(rpc deploymentv1.DeploymentServiceClient) *Client { return &Client{rpc: rpc} }
func (c *Client) Request(ctx context.Context, r Request) (Operation, error) {
	out, err := c.rpc.RequestDeployment(ctx, &deploymentv1.RequestDeploymentRequest{Id: r.ID.String(), RunId: r.RunID.String(), RepoId: r.RepoID.String(), Target: r.Target, Artifact: r.Artifact})
	if err != nil {
		return Operation{}, err
	}
	return operationFromProto(out.GetOperation())
}
func (c *Client) Get(ctx context.Context, id uuid.UUID) (Operation, error) {
	out, err := c.rpc.GetDeployment(ctx, &deploymentv1.GetDeploymentRequest{Id: id.String()})
	if err != nil {
		return Operation{}, err
	}
	return operationFromProto(out.GetOperation())
}
func (c *Client) Execute(ctx context.Context, id uuid.UUID) (Operation, error) {
	out, err := c.rpc.ExecuteDeployment(ctx, &deploymentv1.ExecuteDeploymentRequest{Id: id.String()})
	if err != nil {
		return Operation{}, err
	}
	return operationFromProto(out.GetOperation())
}
func (c *Client) Retry(ctx context.Context, id uuid.UUID) (Operation, error) {
	out, err := c.rpc.RetryDeployment(ctx, &deploymentv1.RetryDeploymentRequest{Id: id.String()})
	if err != nil {
		return Operation{}, err
	}
	return operationFromProto(out.GetOperation())
}
func (c *Client) Reconcile(ctx context.Context, id uuid.UUID) (Operation, error) {
	out, err := c.rpc.ReconcileDeployment(ctx, &deploymentv1.ReconcileDeploymentRequest{Id: id.String()})
	if err != nil {
		return Operation{}, err
	}
	return operationFromProto(out.GetOperation())
}
func operationFromProto(p *deploymentv1.Operation) (Operation, error) {
	if p == nil {
		return Operation{}, errors.New("deployment returned no operation")
	}
	var op Operation
	var err error
	for _, pair := range []struct {
		raw  string
		dest *uuid.UUID
	}{{p.Id, &op.ID}, {p.RunId, &op.RunID}, {p.RepoId, &op.RepoID}, {p.OrgId, &op.OrgID}, {p.ActorId, &op.ActorID}} {
		*pair.dest, err = deploymentID(pair.raw)
		if err != nil {
			return Operation{}, err
		}
	}
	op.Target = p.Target
	op.Artifact = p.Artifact
	op.ActorKind = p.ActorKind
	op.Environment = p.Environment
	op.TargetRevision = p.TargetRevision
	op.Destination = p.Destination
	op.State = p.State
	op.CreatedAt, err = time.Parse(time.RFC3339Nano, p.CreatedAt)
	if err != nil {
		return Operation{}, err
	}
	op.Attempts = []Attempt{}
	op.Credentials = []CredentialObligation{}
	for _, p := range p.Attempts {
		if p == nil {
			return Operation{}, errors.New("deployment returned empty attempt")
		}
		a := Attempt{ActorKind: p.ActorKind, Kind: p.Kind, Number: int(p.Number), State: p.State, Result: Result{ExternalID: p.ExternalId, Summary: p.Summary}, Error: p.Error}
		a.ActorID, err = deploymentID(p.ActorId)
		if err != nil {
			return Operation{}, err
		}
		a.StartedAt, err = time.Parse(time.RFC3339Nano, p.StartedAt)
		if err != nil {
			return Operation{}, err
		}
		if p.FinishedAt != "" {
			v, err := time.Parse(time.RFC3339Nano, p.FinishedAt)
			if err != nil {
				return Operation{}, err
			}
			a.FinishedAt = &v
		}
		op.Attempts = append(op.Attempts, a)
	}
	for _, p := range p.Credentials {
		if p == nil {
			return Operation{}, errors.New("deployment returned empty obligation")
		}
		c := CredentialObligation{Attempt: int(p.Attempt), ProviderBinding: p.ProviderBinding, Phase: p.Phase}
		if p.ResolvedAt != "" {
			v, err := time.Parse(time.RFC3339Nano, p.ResolvedAt)
			if err != nil {
				return Operation{}, err
			}
			c.ResolvedAt = &v
		}
		op.Credentials = append(op.Credentials, c)
	}
	return op, nil
}
