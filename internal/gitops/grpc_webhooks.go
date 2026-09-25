package gitops

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/webhooks"
)

// The webhook RPCs. A hook belongs to a repository, so this service owns them:
// the alternative is another service reading gitplatform.repositories to find out
// whether the repository a hook names exists, which is the cross-schema read this
// platform does not do.
//
// Every one of these resolves the repository through repoByName, which carries the
// organization from the caller's scope. The hook ids are then only ever used inside
// that organization — webhooks.Store applies the predicate again on its own tables,
// so naming another organization's hook id reaches nothing at either layer.

// deliveryHistoryLimit caps a delivery listing when the caller asks for nothing
// sensible. The history is a reading surface, not an export.
const deliveryHistoryLimit = 50

// hooksStore reports the store, or the error to answer with when this deployment
// has no webhook support wired. Saying so is better than a nil dereference, and
// better than an empty list — which would read as "this repository has no hooks".
func (s *Server) hooksStore() (*webhooks.Store, error) {
	if s.Hooks == nil {
		return nil, status.Error(codes.Unimplemented, "this deployment has no webhook support")
	}
	return s.Hooks, nil
}

// hookRepo resolves the repository a webhook request names, in the caller's
// organization.
func (s *Server) hookRepo(ctx context.Context, repo string) (repoRow, error) {
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return repoRow{}, err
	}
	return s.repoByName(ctx, scope.OrgID, repo)
}

func (s *Server) CreateHook(ctx context.Context, req *gitv1.CreateHookRequest) (*gitv1.CreateHookResponse, error) {
	store, err := s.hooksStore()
	if err != nil {
		return nil, err
	}
	row, err := s.hookRepo(ctx, req.GetRepo())
	if err != nil {
		return nil, err
	}
	hook, err := store.CreateHook(ctx, row.ID, req.GetUrl(), req.GetEvents(), req.GetSecret())
	if err != nil {
		return nil, hookError(err)
	}
	return &gitv1.CreateHookResponse{Hook: toProtoHook(hook)}, nil
}

func (s *Server) ListHooks(ctx context.Context, req *gitv1.ListHooksRequest) (*gitv1.ListHooksResponse, error) {
	store, err := s.hooksStore()
	if err != nil {
		return nil, err
	}
	row, err := s.hookRepo(ctx, req.GetRepo())
	if err != nil {
		return nil, err
	}
	hooks, err := store.ListHooks(ctx, row.ID)
	if err != nil {
		return nil, hookError(err)
	}
	out := make([]*gitv1.Hook, 0, len(hooks))
	for _, h := range hooks {
		out = append(out, toProtoHook(h))
	}
	return &gitv1.ListHooksResponse{Hooks: out}, nil
}

func (s *Server) DeleteHook(ctx context.Context, req *gitv1.DeleteHookRequest) (*gitv1.DeleteHookResponse, error) {
	store, err := s.hooksStore()
	if err != nil {
		return nil, err
	}
	// The repository is resolved even though the delete is by hook id: it is what
	// makes the request answerable only inside the caller's organization, and it
	// refuses a request naming a repository that is not theirs before anything is
	// deleted.
	if _, err := s.hookRepo(ctx, req.GetRepo()); err != nil {
		return nil, err
	}
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "hook id is not a uuid")
	}
	if err := store.DeleteHook(ctx, id); err != nil {
		return nil, hookError(err)
	}
	return &gitv1.DeleteHookResponse{}, nil
}

func (s *Server) SetHookSecret(ctx context.Context, req *gitv1.SetHookSecretRequest) (*gitv1.SetHookSecretResponse, error) {
	store, err := s.hooksStore()
	if err != nil {
		return nil, err
	}
	if _, err := s.hookRepo(ctx, req.GetRepo()); err != nil {
		return nil, err
	}
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "hook id is not a uuid")
	}
	if req.GetSetSecret() {
		if err := store.SetSecret(ctx, id, req.GetSecret()); err != nil {
			return nil, hookError(err)
		}
	}
	if req.GetSetActive() {
		if err := store.SetActive(ctx, id, req.GetActive()); err != nil {
			return nil, hookError(err)
		}
	}
	hook, err := store.GetHook(ctx, id)
	if err != nil {
		return nil, hookError(err)
	}
	return &gitv1.SetHookSecretResponse{Hook: toProtoHook(hook)}, nil
}

func (s *Server) ListHookDeliveries(ctx context.Context, req *gitv1.ListHookDeliveriesRequest) (*gitv1.ListHookDeliveriesResponse, error) {
	store, err := s.hooksStore()
	if err != nil {
		return nil, err
	}
	if _, err := s.hookRepo(ctx, req.GetRepo()); err != nil {
		return nil, err
	}
	id, err := uuid.Parse(req.GetHookId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "hook id is not a uuid")
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = deliveryHistoryLimit
	}
	deliveries, err := store.ListDeliveries(ctx, id, limit)
	if err != nil {
		return nil, hookError(err)
	}
	out := make([]*gitv1.HookDelivery, 0, len(deliveries))
	for _, d := range deliveries {
		out = append(out, &gitv1.HookDelivery{
			Id: d.ID.String(), HookId: d.HookID.String(), Event: d.Event,
			StatusCode: int32(d.StatusCode), Error: d.Error, Attempt: int32(d.Attempt),
			At: d.At.UTC().Format(time.RFC3339),
		})
	}
	return &gitv1.ListHookDeliveriesResponse{Deliveries: out}, nil
}

// hookError maps a store failure to a gRPC code. The store's own messages are
// kept: they say which of "no such hook", "not your organization" and "agents may
// not do this" it was, and a bare PermissionDenied would not.
func hookError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, webhooks.ErrNoKEK):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return status.Error(codes.PermissionDenied, err.Error())
	}
}

func toProtoHook(h webhooks.Hook) *gitv1.Hook {
	return &gitv1.Hook{
		Id: h.ID.String(), RepoId: h.RepoID.String(), Url: h.URL,
		Events: h.Events, Active: h.Active, HasSecret: h.HasSecret,
		CreatedAt: h.CreatedAt.UTC().Format(time.RFC3339),
	}
}
