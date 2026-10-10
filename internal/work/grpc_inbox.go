package work

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// The inbox RPCs. Reads and transitions derive the caller's org and recipient
// from authz.FromContext — never from a request field — so an inbox is
// addressable only by its owner. PublishInboxItem is the one ingest path and
// is authorized inside Store.PublishInbox (platform worker, or a caller acting
// inside the org it names).

// inboxStatus maps a store error onto a gRPC code: an authorization refusal is
// PermissionDenied, everything else is this service's fault.
func inboxStatus(err error) codes.Code {
	if strings.Contains(err.Error(), "denied") ||
		errors.Is(err, authz.ErrNoScope) ||
		strings.Contains(err.Error(), "one user's mail") ||
		strings.Contains(err.Error(), "needs a user") {
		return codes.PermissionDenied
	}
	return codes.Internal
}

func toProtoInboxItem(n InboxItem) *workv1.InboxNotification {
	out := &workv1.InboxNotification{
		Id: n.ID.String(), OrgId: n.OrgID.String(), RecipientId: n.RecipientID.String(),
		RepoId: n.RepoID.String(), Reason: n.Reason, Ref: n.Ref, Title: n.Title,
		Body: n.Body, ActorId: n.ActorID.String(), ActorKind: n.ActorKind,
		ActorName: n.ActorName, State: n.State,
		CreatedAt: n.CreatedAt.Format(rfc3339),
	}
	if n.SnoozedUntil != nil {
		out.SnoozedUntil = n.SnoozedUntil.Format(rfc3339)
	}
	return out
}

func (g *GRPCServer) ListInbox(ctx context.Context, req *workv1.ListInboxRequest) (*workv1.ListInboxResponse, error) {
	f := InboxFilter{
		State:  req.GetState(),
		Reason: req.GetReason(),
		Limit:  int(req.GetLimit()),
	}
	if req.GetRepoId() != "" {
		id, err := parseUUID("repo_id", req.GetRepoId())
		if err != nil {
			return nil, err
		}
		f.RepoID = id
	}
	items, err := g.Store.ListInbox(ctx, f)
	if err != nil {
		return nil, status.Errorf(inboxStatus(err), "list inbox: %v", err)
	}
	out := make([]*workv1.InboxNotification, 0, len(items))
	for _, n := range items {
		out = append(out, toProtoInboxItem(n))
	}
	return &workv1.ListInboxResponse{Notifications: out}, nil
}

// InboxUnread feeds the header badge. It deliberately derives no organization:
// see Store.InboxUnread for why the count spans the caller's organizations.
func (g *GRPCServer) InboxUnread(ctx context.Context, _ *workv1.InboxUnreadRequest) (*workv1.InboxUnreadResponse, error) {
	n, err := g.Store.InboxUnread(ctx)
	if err != nil {
		return nil, status.Errorf(inboxStatus(err), "inbox unread: %v", err)
	}
	return &workv1.InboxUnreadResponse{Unread: int32(n)}, nil
}

func (g *GRPCServer) InboxDone(ctx context.Context, req *workv1.InboxDoneRequest) (*workv1.InboxDoneResponse, error) {
	id, err := parseUUID("id", req.GetId())
	if err != nil {
		return nil, err
	}
	if err := g.Store.InboxDone(ctx, id); err != nil {
		return nil, status.Error(codes.NotFound, "notification not found")
	}
	return &workv1.InboxDoneResponse{}, nil
}

func (g *GRPCServer) InboxSnooze(ctx context.Context, req *workv1.InboxSnoozeRequest) (*workv1.InboxSnoozeResponse, error) {
	id, err := parseUUID("id", req.GetId())
	if err != nil {
		return nil, err
	}
	until, perr := time.Parse(time.RFC3339, req.GetUntil())
	if perr != nil {
		return nil, status.Error(codes.InvalidArgument, "until must be RFC3339")
	}
	if err := g.Store.InboxSnooze(ctx, id, until); err != nil {
		return nil, status.Error(codes.NotFound, "notification not found")
	}
	return &workv1.InboxSnoozeResponse{}, nil
}

func (g *GRPCServer) InboxSave(ctx context.Context, req *workv1.InboxSaveRequest) (*workv1.InboxSaveResponse, error) {
	id, err := parseUUID("id", req.GetId())
	if err != nil {
		return nil, err
	}
	if err := g.Store.InboxSave(ctx, id, req.GetSaved()); err != nil {
		return nil, status.Error(codes.NotFound, "notification not found")
	}
	return &workv1.InboxSaveResponse{}, nil
}

// PublishInboxItem is the ingest path the platform's other services call —
// the gates service when a change's diff raises an approval request. The org
// is a request field because a publisher names the organization of the run it
// observed; Store.PublishInbox refuses anyone but a platform worker or a
// member of that same organization, so the field cannot be used to reach into
// an org the caller does not act in.
func (g *GRPCServer) PublishInboxItem(ctx context.Context, req *workv1.PublishInboxItemRequest) (*workv1.PublishInboxItemResponse, error) {
	orgID, err := parseUUID("org_id", req.GetOrgId())
	if err != nil {
		return nil, err
	}
	repoID := uuid.Nil
	if req.GetRepoId() != "" {
		repoID, err = parseUUID("repo_id", req.GetRepoId())
		if err != nil {
			return nil, err
		}
	}
	actorID := uuid.Nil
	if req.GetActorId() != "" {
		actorID, err = parseUUID("actor_id", req.GetActorId())
		if err != nil {
			return nil, err
		}
	}
	recipients := make([]uuid.UUID, 0, len(req.GetRecipients()))
	for _, raw := range req.GetRecipients() {
		id, err := parseUUID("recipients", raw)
		if err != nil {
			return nil, err
		}
		recipients = append(recipients, id)
	}
	created, err := g.Store.PublishInbox(ctx, orgID, InboxItem{
		RepoID: repoID, Reason: req.GetReason(), Ref: req.GetRef(),
		Title: req.GetTitle(), Body: req.GetBody(),
		ActorID: actorID, ActorKind: req.GetActorKind(), ActorName: req.GetActorName(),
		DedupeKey: req.GetDedupeKey(),
	}, recipients)
	if err != nil {
		return nil, status.Errorf(inboxStatus(err), "publish inbox: %v", err)
	}
	return &workv1.PublishInboxItemResponse{Inserted: len(created) > 0}, nil
}
