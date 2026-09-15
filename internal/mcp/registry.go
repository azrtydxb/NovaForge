package mcp

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	mcpv1 "github.com/novaforge/novaforge/gen/novaforge/mcp/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// Registry implements mcpv1.McpServiceServer: an organization's register of
// the external MCP servers its agents may be offered.
//
// Approval is a separate step from asking because an external MCP server is a
// channel from outside the organization straight into an agent's context. Any
// member may ask; only an owner or admin may say yes, and a yes can be taken
// back. Roles live in the identity schema, so they are read through identity's
// RPC with the caller's own credential, never from a table here.
type Registry struct {
	mcpv1.UnimplementedMcpServiceServer

	pool     *pgxpool.Pool
	identity identityv1.IdentityServiceClient
}

// NewRegistry returns a Registry over the mcp schema in pool.
func NewRegistry(pool *pgxpool.Pool, identity identityv1.IdentityServiceClient) *Registry {
	return &Registry{pool: pool, identity: identity}
}

const (
	statusPending  = "pending"
	statusApproved = "approved"
	statusRejected = "rejected"
	statusRevoked  = "revoked"
)

var (
	serverNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	transports   = map[string]bool{"stdio": true, "streamable_http": true}
	statuses     = map[string]bool{statusPending: true, statusApproved: true, statusRejected: true, statusRevoked: true}
	// deciderRoles are the organization roles that may approve, reject and
	// revoke. identity creates an organization's first member as "owner".
	deciderRoles = map[string]bool{"owner": true, "admin": true}
)

const serverColumns = `id, org_id, name, url, transport, description, status, requested_by, decided_by, decided_at, reason, created_at`

func orgScope(ctx context.Context) (authz.Scope, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return authz.Scope{}, status.Error(codes.PermissionDenied, "no authorization scope for this call")
	}
	return scope, nil
}

// personScope admits a person. An agent asking for access to a new external
// server for itself is exactly the escalation this register exists to stop,
// and a service token names nobody to hold to account for the request.
func personScope(ctx context.Context) (authz.Scope, error) {
	scope, err := orgScope(ctx)
	if err != nil {
		return authz.Scope{}, err
	}
	if scope.ActorKind != "user" || scope.ActorID == uuid.Nil {
		return authz.Scope{}, status.Errorf(codes.PermissionDenied, "only an organization member may do this, not a %q caller", scope.ActorKind)
	}
	return scope, nil
}

// role returns the caller's role in their scoped organization, as identity
// records it.
func (r *Registry) role(ctx context.Context, scope authz.Scope) (string, error) {
	if r.identity == nil {
		return "", status.Error(codes.Unimplemented, "this deployment has no identity client wired for role checks")
	}
	resp, err := r.identity.ListOrgMembers(ctx, &identityv1.ListOrgMembersRequest{Org: scope.OrgID.String()})
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() != codes.Unknown {
			return "", s.Err()
		}
		return "", status.Errorf(codes.Internal, "look up organization role: %v", err)
	}
	for _, m := range resp.GetMembers() {
		if m.GetUserId() == scope.ActorID.String() {
			return m.GetRole(), nil
		}
	}
	return "", status.Error(codes.PermissionDenied, "not a member of this organization")
}

// requireDecider admits an owner or admin of the scoped organization.
func (r *Registry) requireDecider(ctx context.Context) (authz.Scope, error) {
	scope, err := personScope(ctx)
	if err != nil {
		return authz.Scope{}, err
	}
	role, err := r.role(ctx, scope)
	if err != nil {
		return authz.Scope{}, err
	}
	if !deciderRoles[role] {
		return authz.Scope{}, status.Errorf(codes.PermissionDenied, "only an organization owner or admin may decide on MCP servers; your role is %q", role)
	}
	return scope, nil
}

// RequestServer records a pending request for an external MCP server.
func (r *Registry) RequestServer(ctx context.Context, req *mcpv1.RequestServerRequest) (*mcpv1.RequestServerResponse, error) {
	scope, err := personScope(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if !serverNameRe.MatchString(name) {
		return nil, status.Errorf(codes.InvalidArgument, "name %q must be lowercase letters, digits, '.', '_' or '-', starting with a letter or digit", req.GetName())
	}
	if !transports[req.GetTransport()] {
		return nil, status.Errorf(codes.InvalidArgument, "transport must be \"stdio\" or \"streamable_http\", not %q", req.GetTransport())
	}
	endpoint := strings.TrimSpace(req.GetUrl())
	switch req.GetTransport() {
	case "streamable_http":
		// Only an http(s) URL is a Streamable HTTP endpoint. Anything else —
		// file://, a bare host — would be approved as one thing and dialled
		// as another.
		u, err := url.Parse(endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, status.Errorf(codes.InvalidArgument, "a streamable_http server needs an http or https URL, not %q", req.GetUrl())
		}
	case "stdio":
		if endpoint == "" {
			return nil, status.Error(codes.InvalidArgument, "a stdio server needs the command that launches it")
		}
	}

	row := r.pool.QueryRow(ctx, `
		INSERT INTO mcp.mcp_servers (id, org_id, name, url, transport, description, status, requested_by)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7)
		RETURNING `+serverColumns,
		uuid.New(), scope.OrgID, name, endpoint, req.GetTransport(), strings.TrimSpace(req.GetDescription()), scope.ActorID)
	s, err := scanServer(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, status.Errorf(codes.AlreadyExists, "an MCP server named %q is already registered in this organization", name)
		}
		return nil, status.Errorf(codes.Internal, "record MCP server request: %v", err)
	}
	return &mcpv1.RequestServerResponse{Server: s}, nil
}

// ListServers lists the organization's registered servers, optionally only
// those in one status. Any scoped caller may list: which servers are approved
// is what an agent's host needs to read.
func (r *Registry) ListServers(ctx context.Context, req *mcpv1.ListServersRequest) (*mcpv1.ListServersResponse, error) {
	scope, err := orgScope(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetStatus() != "" && !statuses[req.GetStatus()] {
		return nil, status.Errorf(codes.InvalidArgument, "unknown status %q", req.GetStatus())
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+serverColumns+`
		  FROM mcp.mcp_servers
		 WHERE org_id = $1 AND ($2 = '' OR status = $2)
		 ORDER BY created_at DESC, name`,
		scope.OrgID, req.GetStatus())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list MCP servers: %v", err)
	}
	defer rows.Close()
	out := []*mcpv1.McpServer{}
	for rows.Next() {
		s, err := scanServer(rows)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "read MCP server: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "list MCP servers: %v", err)
	}

	// can_decide is computed here rather than by the screen, so there is one
	// definition of who decides. A failed lookup means "no", never an error
	// that hides the list from someone entitled to read it.
	canDecide := false
	if scope.ActorKind == "user" && scope.ActorID != uuid.Nil {
		if role, err := r.role(ctx, scope); err == nil {
			canDecide = deciderRoles[role]
		}
	}
	return &mcpv1.ListServersResponse{Servers: out, CanDecide: canDecide}, nil
}

// ListApprovedServers lists the servers an agent in the caller's organization
// may be offered: approved, and not since revoked. Any scoped caller may ask,
// including an agent run's own credential, since the host building a run's
// tool list is acting for that run.
func (r *Registry) ListApprovedServers(ctx context.Context, _ *mcpv1.ListApprovedServersRequest) (*mcpv1.ListApprovedServersResponse, error) {
	scope, err := orgScope(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+serverColumns+`
		  FROM mcp.mcp_servers
		 WHERE org_id = $1 AND status = 'approved'
		 ORDER BY name`,
		scope.OrgID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list approved MCP servers: %v", err)
	}
	defer rows.Close()
	out := []*mcpv1.McpServer{}
	for rows.Next() {
		s, err := scanServer(rows)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "read MCP server: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "list approved MCP servers: %v", err)
	}
	return &mcpv1.ListApprovedServersResponse{Servers: out}, nil
}

// DecideServer approves or rejects a pending server.
func (r *Registry) DecideServer(ctx context.Context, req *mcpv1.DecideServerRequest) (*mcpv1.DecideServerResponse, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid id %q", req.GetId())
	}
	scope, err := r.requireDecider(ctx)
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(req.GetReason())
	next := statusApproved
	if !req.GetApprove() {
		next = statusRejected
		// A rejection with no reason leaves the requester nothing to act on.
		if reason == "" {
			return nil, status.Error(codes.InvalidArgument, "a rejection needs a reason")
		}
	}
	s, err := r.transition(ctx, scope, id, statusPending, next, reason)
	if err != nil {
		return nil, err
	}
	return &mcpv1.DecideServerResponse{Server: s}, nil
}

// RevokeServer withdraws an approval.
func (r *Registry) RevokeServer(ctx context.Context, req *mcpv1.RevokeServerRequest) (*mcpv1.RevokeServerResponse, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid id %q", req.GetId())
	}
	scope, err := r.requireDecider(ctx)
	if err != nil {
		return nil, err
	}
	s, err := r.transition(ctx, scope, id, statusApproved, statusRevoked, strings.TrimSpace(req.GetReason()))
	if err != nil {
		return nil, err
	}
	return &mcpv1.RevokeServerResponse{Server: s}, nil
}

// transition moves one server from one status to the next in a single
// conditional UPDATE, so two admins deciding at once cannot both succeed and
// a stale screen cannot re-decide a server someone else already decided.
func (r *Registry) transition(ctx context.Context, scope authz.Scope, id uuid.UUID, from, to, reason string) (*mcpv1.McpServer, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE mcp.mcp_servers
		   SET status = $1, decided_by = $2, decided_at = now(), reason = $3
		 WHERE id = $4 AND org_id = $5 AND status = $6
		RETURNING `+serverColumns,
		to, scope.ActorID, reason, id, scope.OrgID, from)
	s, err := scanServer(row)
	if err == nil {
		return s, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "update MCP server: %v", err)
	}
	var current string
	err = r.pool.QueryRow(ctx,
		`SELECT status FROM mcp.mcp_servers WHERE id = $1 AND org_id = $2`, id, scope.OrgID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Errorf(codes.NotFound, "no MCP server %s in this organization", id)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read MCP server: %v", err)
	}
	return nil, status.Errorf(codes.FailedPrecondition, "MCP server %s is %s, not %s", id, current, from)
}

func scanServer(row pgx.Row) (*mcpv1.McpServer, error) {
	var (
		s                mcpv1.McpServer
		id, orgID, reqBy uuid.UUID
		decidedBy        *uuid.UUID
		decidedAt        *time.Time
		createdAt        time.Time
	)
	if err := row.Scan(&id, &orgID, &s.Name, &s.Url, &s.Transport, &s.Description, &s.Status,
		&reqBy, &decidedBy, &decidedAt, &s.Reason, &createdAt); err != nil {
		return nil, err
	}
	s.Id, s.OrgId, s.RequestedBy = id.String(), orgID.String(), reqBy.String()
	if decidedBy != nil {
		s.DecidedBy = decidedBy.String()
	}
	if decidedAt != nil {
		s.DecidedAt = decidedAt.UTC().Format(time.RFC3339)
	}
	s.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	return &s, nil
}
