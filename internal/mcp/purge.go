package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/authz"
)

// PurgeOrganization deletes every external MCP server registered for the
// caller's organization.
func (r *Registry) PurgeOrganization(ctx context.Context) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return errors.New("purge requires an organization scope")
	}
	if _, err := r.pool.Exec(ctx, `DELETE FROM mcp.mcp_servers WHERE org_id = $1`, scope.OrgID); err != nil {
		return fmt.Errorf("purge mcp servers: %w", err)
	}
	return nil
}
