-- An organization's register of external MCP servers. A row starts pending
-- and only an owner or admin moves it on; the history of who decided, when
-- and why stays on the row rather than being overwritten by a re-request.
CREATE TABLE IF NOT EXISTS mcp_servers (
    id           uuid PRIMARY KEY,
    org_id       uuid NOT NULL,
    name         text NOT NULL,
    url          text NOT NULL,
    transport    text NOT NULL CHECK (transport IN ('stdio', 'streamable_http')),
    description  text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'approved', 'rejected', 'revoked')),
    requested_by uuid NOT NULL,
    decided_by   uuid,
    decided_at   timestamptz,
    reason       text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);

CREATE INDEX IF NOT EXISTS mcp_servers_org_status ON mcp_servers (org_id, status);
