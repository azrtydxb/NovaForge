package identity

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/events"
)

// OrgDeletedPublisher announces an organization's deletion to every service.
type OrgDeletedPublisher func(ctx context.Context, e events.OrgDeletedEvent) error

// RedisOrgDeletedPublisher publishes on events.StreamOrgDeleted.
func RedisOrgDeletedPublisher(rdb *redis.Client) OrgDeletedPublisher {
	return func(ctx context.Context, e events.OrgDeletedEvent) error {
		return events.Publish(ctx, rdb, events.StreamOrgDeleted, e)
	}
}

// SetOrgDeletedPublisher wires how DeleteOrg announces a deletion. Until it is
// set DeleteOrg deletes nothing.
func (s *Server) SetOrgDeletedPublisher(p OrgDeletedPublisher) { s.orgDeleted = p }

// DeleteOrg deletes an organization on its owner's word.
//
// There was no way to do this but hack/purge-orgs.sh, which reached into
// every service's schema from one transaction — the one thing no service may
// do. The deletion is now announced on events.StreamOrgDeleted, each service
// removes its own share, and identity removes the organization and its
// memberships. It is announced before the organization is removed: once the
// row is gone nothing could say what to clean up, and a failure after the
// announcement leaves an organization its owner asked to delete, which a
// second request finishes. User accounts are not deleted — they belong to
// people, who may belong to other organizations or join one later.
func (s *Server) DeleteOrg(ctx context.Context, req *identityv1.DeleteOrgRequest) (*identityv1.DeleteOrgResponse, error) {
	callerID, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	org, err := s.store.OrgByNameOrID(ctx, req.GetOrg())
	if err != nil {
		// Not a member and no such organization read the same.
		return nil, status.Error(codes.PermissionDenied, "not an owner of this organization")
	}
	role, err := s.store.MemberRole(ctx, org.ID, callerID)
	if err != nil || role != "owner" {
		return nil, status.Error(codes.PermissionDenied, "only an owner may delete an organization")
	}
	if req.GetConfirmName() != org.Name {
		return nil, status.Error(codes.InvalidArgument, "confirm_name must be the organization's name")
	}
	if s.orgDeleted == nil {
		return nil, status.Error(codes.FailedPrecondition, "this deployment cannot announce an organization's deletion, so nothing was deleted")
	}
	if err := s.orgDeleted(ctx, events.OrgDeletedEvent{OrgID: org.ID, OrgName: org.Name, At: time.Now().UTC()}); err != nil {
		return nil, status.Errorf(codes.Unavailable, "announce the deletion to the services that hold its data: %v; nothing was deleted", err)
	}
	if err := s.store.DeleteOrg(ctx, org.ID); err != nil {
		return nil, status.Errorf(codes.Internal, "delete organization: %v", err)
	}
	return &identityv1.DeleteOrgResponse{Org: &identityv1.Org{Id: org.ID.String(), Name: org.Name}}, nil
}

// errOrgNotDeleted is returned when no organization row was removed.
var errOrgNotDeleted = errors.New("organization not found")

// DeleteOrg removes an organization; its memberships cascade.
func (s *Store) DeleteOrg(ctx context.Context, orgID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM identity.organizations WHERE id = $1`, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errOrgNotDeleted
	}
	return nil
}
