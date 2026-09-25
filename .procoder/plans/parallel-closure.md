# Parallel completion closure

## Goal and authority

User instruction: close all gaps with parallel agents, without deferral.
Baseline: main `764c283`; implementation `6b48f80` deployed at revision 83.
No open GitHub issues or pull requests were found at launch. Existing historical
worktrees remain untouched. Product/security choices requiring new authority
must be escalated rather than guessed. Existing authorization covers verified
integration commits and deployment to kw, with normal preflight and Helm ownership.

## Ownership board

All writer worktrees start at the baseline and live below
`/Users/pascal/Development/NovaForge-lanes/`. Each has one writer; reviews are
read-only. Workers do not push, deploy, close tasks or modify shared evidence docs.

| Lane / branch          | Worktree suffix | Exclusive edit boundary                                                                           | Delivery / next gate                                                                         |
| ---------------------- | --------------- | ------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| closure/index-safety   | index-safety    | internal/indexing, graph store/vector/purge/fence internals, internal/cleanup                     | Fenced writes and deletion coordination; real-PG failure tests; fresh review                 |
| closure/deployments    | deployments     | new internal/deployment, internal/approvals, deployment-specific tools/tests; no credential files | Actual approved deployment execution and evidence; real execution tests; fresh review        |
| closure/credentials    | credentials     | internal/secrets, internal/ci credential files and credential tests                               | Underlying credential expiry/scoping; real provider tests; fresh review                      |
| closure/semantic-index | semantic-index  | new internal/semanticindex plus new adapters/tests; no existing graph/indexing files              | LSP/SCIP and broader-language ingestion; real-tool tests; integration handoff; fresh review  |
| closure/gui            | gui             | web, new GUI-specific edge handlers/tests; no shared route registration                           | Finish design-to-GUI coverage and tests; fresh review                                        |
| closure/runtime-policy | runtime-policy  | internal/agentrun, agents, workspace, tools MCP files, runtime-specific configuration/tests       | Budget, confinement and MCP runtime gaps; real-service tests; fresh review                   |
| completion-audit       | main, read-only | Design, spec, six plans, BUILD-STATUS, configuration consumers and acceptance                     | Exhaustive requirement-to-evidence matrix; identify omissions and dependencies               |
| parent integration     | main            | Shared proto/generated interfaces, service wiring, Helm, shared edge routes, evidence docs        | Synthesize all handoffs, resolve integration, full tests/gate, deploy and cluster acceptance |

Approved supervisor scope updates:

- GUI also owns `internal/work`, `internal/reviews`, their work/reviews proto files
  and only their generated outputs, plus new `internal/edge/gui_*` handlers/tests.
  Parent retains shared routes, OpenAPI and entrypoint integration. Required paths
  include Agent Run tools/events, Engineering Run reviews, and Work Item PATCH
  and state transitions using existing authorization/state-machine rules.
- Runtime policy also owns `internal/mcp`; parent retains shared configuration.
- Credentials also owns the gates proto and only its generated outputs, plus new
  credential RPC handlers/tests in `internal/gates`. Implement broker-owned atomic
  issuance fencing and durable provider revocation, not client list-then-revoke.
  Terminal closure fences the job; partial resolution cleanup fences only its
  attempt so legitimate retries remain possible. Parent owns CI lifecycle callers,
  shared gates wiring and durable retry activation. Pending provider cleanup must
  remain visible, and late issuance must never deliver credentials after closure.
- Nested-module evidence includes explicit `ModulePath` in addition to raw
  `go.mod` content hashes. Parent wires `GoFileModulePaths` and
  `GoFileModuleHashes` with exact source-manifest key sets. Missing evidence is
  not equivalent to known absence (`""` path and SHA256(nil)). Moving unchanged
  module metadata must invalidate evidence; legacy paths are never guessed.
- Graph migration 000005 belongs to index-safety; secrets migration 000003 belongs
  to credentials. Migration-changing tests use randomly named, lane-owned
  disposable PostgreSQL databases, never shared-schema rollback or forced drops.
- Credentials may download a pinned official OpenBao binary into a lane-owned
  temporary directory for verified loopback-only disposable-provider tests.
  Verify official checksum/signature provenance; no global install, shared issuer
  mutation, or use of another project's tokens is authorized.

Workers request ownership changes through the supervisor before editing another
lane's files. Shared migration ordering, proto contracts and entrypoint changes
are parent-owned. Integration requirements are delivered as exact proposed patches
or file/symbol instructions, not silently deferred features.

## Acceptance

- Each behavioral change has a regression observed red, then green.
- Datastore tests source the original repository's hack/env.sh without printing
  credentials. No global truncation or unrelated data deletion.
- Mutation snapshots are taken immediately before each mutation and restored
  immediately after it.
- Each writer hands off changes, commands/results, evidence paths, integration
  contracts and any unresolved decisions. Fresh reviewers inspect actual diffs.
- Parent validates every lane and the combined result; Procoder test, lint,
  security and check precede completion and deployment.
- Complete immutable images, normal preflight and all applicable cluster suites
  are required. Closed historical tasks and passing component tests do not
  establish whole-product completion.

## Status

All six implementation checkpoints have been delivered. Independent correction
reviews accept all six lanes as bounded components for parent integration;
this is not merged-tree acceptance.
GUI's final F2/P2 mixed PAT/session 403-to-401 downgrade is independently resolved
(review follow-up `92e97ba5`, report under `4ad05a29`).

Runtime review follow-up `2cb70e28` accepted R1/R2/R3 corrections: every opened
stdio stream retains cleanup responsibility, independent exit notification cancels
the run, and stale acknowledgements cannot report pending cleanup as complete.
R2 acceptance is bounded by notification delivery, not a promise of zero latency
from remote exit or proof of container death. The original accounting, allowlist,
starvation and overflow corrections also passed component review. The full tools
suite remains red on its shared fixture's missing mandatory admission dependencies.
All lane writers are checkpointed. Parent imported all 251 reviewed paths onto
`closure/integration` after confirming no cross-lane file overlaps, stable source
hashes, non-conflicting parent files and clean patch applicability. Imported bytes
match the per-lane manifest. This is checkpoint convergence, not completed wiring
or integrated acceptance; nothing is staged or committed.

Parent has pinned `go-ai-sdk v0.5.0`; `go build ./...` passes on the combined
checkpoint. This is compilation evidence only, not test execution or configured
coding-plan acceptance. No deployment occurred: deployed implementation remains
`6b48f80`, revision 83. Canonical combined tests/gates and cluster acceptance
remain open.

Immediate integration requirements include authenticated capability-owner and
Work claim adapters, frozen-intent consumption and scheduler admission replacement,
ordinary-workspace termination before Work release, cleanup and accounting API/UI
visibility, provider/janitor and CI lifecycle wiring (including deleted orgs),
canonical review-history routing, Identity datastore error classification,
exact-session logout, atomic Git source CAS, independent-review invocation,
authenticated streaming, exact semantic producer/storage and module evidence.
Runtime claim/release types still need the Work owner's sponsor identity and
explicit no-execution guarantee fields; outcome strings alone are not that proof.

OpenBao's tested PostgreSQL credentials remained valid 4.574643 seconds beyond the
provider lease timestamp during issuer outage. Hard target expiry and existing
session invalidation must not be claimed from those tests. The user has now
authorized a dedicated NovaForge-only OpenBao and isolated staging/production Helm
acceptance releases, not mutation of unrelated issuers or applications. Provisioning,
configuration and target-enforcement proof remain to be done.

## Integration wave and exhaustive acceptance leaves

Five new isolated writers started from the identical combined checkpoint, in
`/Users/pascal/Development/NovaForge-integration-lanes/<lane>`, branches
`integrate/<lane>`, HEAD `764c28320f0adf7976340937c9ca1ce8aa40f5ea`:

- `runtime-auth`: Identity capability owner RPCs and exact-session logout, runtime
  admission/Work adapters, workspace lifecycle, API accounting, real executor fixtures.
- `ci-governance`: secrets startup, durable CI cleanup/redaction, runner incarnation,
  gates owner registration and exact source/target-bound merge authorization.
- `graph-producer`: isolated semantic producer/storage, exact module maps, Work RPC
  context, revision-pinned declared graph metadata with explicit provenance.
- `edge-reviews`: permanent routes/OpenAPI, inspected-SHA review/CLI, atomic Git CAS,
  bounded durable independent reviews and actual UI/stream consumers.
- `delivery`: DeploymentService on the existing gates listener, fixed Helm executor,
  owner credential adapter, materialization fence and observed-success event outbox.

Workflow `a203c772-c0f8-43b3-9c3a-afb75c8b73ac` completed its read-only exhaustive
audit; all five writers reached their 1,800,000 ms time limit. No partial checkpoint
is accepted as finished. Parent preserved dirty bytes, modes, hashes and diffs under
`/var/folders/s9/b7bq29kx4nvc5cp4x4jndd0h0000gn/T/novaforge-integration-timeout-1mpfuivb`.
All five retained sessions were resumable and continued natively in workflow
`bb1fe4ad-315c-479b-8d80-70713ca05eca`, unchanged worktrees/models/protocol, with
stable-interface publication and bounded test checkpoints prioritized.

The full source audit is retained in `completion-proof-audit.md`: 46 explicit
acceptance leaves reconcile all 33 backend criteria, six plans/67 historical tasks,
28 design sections and operational limits. These are OPEN acceptance work, not
newly closed tasks. In particular, the current five writers are not the entire
completion scope. Parent owns prioritizing the exposed privileged gate-execution
boundary (L01), then omitted rate limits, strict repository policy/configuration,
full offline gates/language support, teams, retention, pagination/transport/storage
and event durability, enterprise-scale proof, SDK responsibilities/coding-plan
configuration, complete admin/API contracts and final evidence reconciliation.
Any subsequent ownership transfer must name files and avoid a second writer.

Parent added and red/green tested five mounted operator-config paths in the actual
`internal/service/service.go`: OpenBao, deployments, semantic producer, MCP HTTP and
independent reviews. `go test -race ./internal/service -count=1` passed, recorded in
`/tmp/novaforge-operator-config-final-green.log`. This proves the config contract,
not chart mounts or configured services. Canonical security found two advisory
findings and no blocking findings; that does not supersede the source audit's
privileged gate execution defect or constitute a release gate.
