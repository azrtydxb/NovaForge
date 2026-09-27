# Broader intelligence disposition — G10

Source checkpoint: 2bb3a65. This audit does not upgrade acceptance coverage or
claim cluster behavior from unit/package tests. G10 explicitly permits bounded
follow-ups; the remaining qualification work is recorded in the linked task.

| Area | Observed implementation | Disposition |
| --- | --- | --- |
| Index/graph breadth | `internal/indexing/parse.go` uses Tree-sitter Go, Java, Python and TypeScript grammars. `internal/semanticindex/producer_config.go` pins SCIP Go/TypeScript/Python and gopls commands, immutable images, resource bounds and offline execution. `cmd/engineering-graph/main.go` constructs the producer only with operator configuration. | Qualify the configured producer by language, edge kind and branch before calling semantic coverage complete. Unsupported/failed parses must remain explicit. |
| Knowledge recall | `cmd/agent-runtime/main.go` supplies Graph and Knowledge clients. `internal/agentrun/prepare.go` assembles prior knowledge into the initial brief, preserving context gaps. `TestKnowledgeRecall` in `runner_test.go` exercises the real storage/service path. | Cluster evidence of a recorded decision entering a later relevant run remains a qualification follow-up; retrieval is not an unlimited memory guarantee. |
| External MCP | HTTP bearer resolution and isolated stdio lifecycle exist in `internal/tools/mcp.go`, `internal/mcp/client.go` and runner options. The production runner initializer does not supply `MCPOptions`; therefore approval alone does not activate these integrations. | Wire operator-bound credential references and sandbox session ownership before activation. Keep missing policy/transport refused. |
| Agent CI sponsorship | `internal/ci/agentjobs.go` explicitly refuses agent jobs when an agent/service push supplies no human sponsor. | Preserve this scope decision. Delegated sponsorship requires a separate policy design binding an accountable human, repository, action, expiry and revocation; never synthesize a human actor. |
| Workspace dependencies | Semantic producer environment disables Go network/module downloads; agent workspaces retain network isolation. | Publish and qualify a vendored-dependency/operator-cache workflow. Repository instructions cannot enable outbound internet or select privileged images. |
| Cost budgets | Production parses model prices and supplies the default model price; unpriced cost-limited runs are refused. Runner supports `PriceForModel`, but production initializer only supplies `Price`. | Qualify default-model budget accounting on cluster and wire lookup for repository-selected priced models. Preserve refusal for missing prices and overflow. |
| Dependency scanning | Dependency-change approval recognizes go.mod, package.json, Cargo.toml and requirements*.txt in `internal/gates/changes.go`; that is distinct from OSV vulnerability lockfile coverage. Offline scanner validates advisory manifest age/digests. | Publish a tested ecosystem/lockfile matrix and operator refresh procedure with explicit unsupported cases and expired-snapshot refusal. |
| Direct default-branch push | Ordinary authorized member Git pushes remain supported. Deployment has separate immutable intent, independent approval and provider credential boundaries. | Preserve current product scope: a push is not production deployment authorization. Branch-protection policy would be a separate feature, not an implicit change to member rights. |

Follow-up acceptance is tracked in
[`20260927-intelligence-production-qualification.md`](../todo/20260927-intelligence-production-qualification.md).
These items remain open rather than being hidden behind the historical 43/47 count.
