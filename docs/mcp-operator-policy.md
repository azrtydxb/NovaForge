# Operator-bound agent MCP

Organization approval and repository tool selection do not supply credentials or
commands. The agent runtime also requires an operator policy, mounted by
`operatorConfigs.mcpHTTP.secretName` / `configKey` in Helm. Despite the historical
name, this configuration governs both transports. Empty configuration refuses
external MCP. Changes to binding identity require a runtime restart; removal or
change revokes an existing client's next call immediately after the mounted file
updates. Kubernetes projected Secret updates are asynchronous.

Example `config.json` (replace identifiers and destination):

```json
{
  "bindings": [
    {
      "org_id": "11111111-1111-4111-8111-111111111111",
      "repo_id": "22222222-2222-4222-8222-222222222222",
      "server_id": "33333333-3333-4333-8333-333333333333",
      "url": "https://mcp.internal.example/mcp",
      "transport": "streamable_http",
      "token_file": "bearer-token",
      "ca_file": "ca.pem",
      "cidrs": ["10.42.0.0/16"]
    }
  ]
}
```

The token and optional PEM CA bundle are sibling Secret keys. Token bytes are
read for every request; deletion, unreadable content or empty content fails
closed. Bearer credentials require HTTPS. CA roots are loaded when the client is
created; restart runs when rotating trust roots. Public servers must explicitly
set `public: true` and omit `token_file`. Destinations reject credentials in URLs,
query strings and fragments. Egress resolves and pins only an approved address,
disables ambient proxies and refuses redirects, loopback and metadata addresses.
Private IPs require a matching private CIDR. Cluster network policy must also
permit the exact required destination; repository settings cannot change it.

For stdio use `transport: "stdio"`, the exact approved registration's `url`, and
`command: ["/absolute/operator/executable", "argument"]`. Omit HTTP fields. The
command runs through Kubernetes exec in the current run's isolated workspace,
never a host subprocess. Its binary must already be in the operator workspace
image or workspace. Repository-provided executable content remains untrusted and
has only the workspace's existing isolation. Sessions record their workspace
identity before execution. Cancellation and completion require observed container
termination before acknowledging cleanup; unavailable proof retains the obligation.

Each tool call rechecks organization approval and operator binding identity. HTTP
requests additionally recheck policy and credentials, including discovery requests.
Raw bearer values echoed in decoded MCP results, discovery or errors are redacted
before reaching the model. This prevents direct credential echoes, not arbitrary
encodings invented by a malicious server; approve servers as credential recipients.
No bearer bytes are put in argv or normal logs.

Production wiring is covered by executable seam and policy tests. Full cluster
transport/cancellation qualification remains tracked in the intelligence task;
this document does not claim that qualification has finished.
