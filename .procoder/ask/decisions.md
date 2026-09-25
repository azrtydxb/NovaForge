# Decisions

All product choices below are answered. Parallel closure uses restricted human
Work Item edits, OpenBao credential issuance and Helm workload deployment.
Connection and target configuration still require verification before live acceptance.

## Runtime settlement and issuance safety — supervisor implementation contract

Approved within existing correction authority, not a new product choice or grant
of delegation rights. Runtime worker `91ae86a1` owns only the result-settlement
and cancellation/preparation-classification changes in `cmd/agent-runtime/main.go`
and focused caller tests in its isolated worktree; other startup wiring remains
parent-owned.

- Atomic agents-owned completion stores outcome, measured spend with availability,
  summary evidence and cleanup intent together. Identical retries are idempotent;
  stale/conflicting completion cannot overwrite accounting or cancellation.
  Persistence failure retains the result and blocks downstream Engineering Run
  creation. Bounded in-memory retry alone is not durable crash recovery.
- Persist immutable issuance intent before the owner RPC. Parent-owned grant
  issuance must support a stable caller-generated identity bound to authenticated
  org/issuer/run, subject kind/id, capabilities and fixed absolute expiry. Exact
  replay cannot extend, replace or resurrect revoked/expired authority. Mismatches
  and cross-org collisions fail closed without disclosure.
- Reconcile ambiguous issuance and cancellation/deletion races through an explicit
  owner issuance-cancellation fence, not ordinary `Revoke(id)` returning NotFound.
  The owner atomically records a durable tombstone and revokes any matching grant
  under the same serialization as issuance; late issue/replay cannot resurrect it.
  Bind org, issuer, run and immutable issuance intent; guessed or mismatched IDs
  cannot authorize cancellation. Stored issuer identifiers are provenance, never
  authentication: background cleanup uses a separately authenticated narrow owner
  adapter, without persisted bearer tokens or blanket service delegation.
  Missing/unconfirmed fencing retains pending cleanup and blocks purge. Recovery
  claims only stale unresolved issuance, not healthy concurrent queued starts.
  Owner implementation and authenticated acceptance remain parent-owned.
  No cross-schema SQL or transaction may span external RPC.
  The worker may implement the consumer contract, not the capability owner/RPC.
- Runtime worker also owns the narrow repository tool-name validator and focused
  tests: exact MCP qualified names may pass syntax validation, but never grant
  authority. Use the same unambiguous grammar as registration; reject wildcards,
  empty components and ambiguous identities. Intersect repository selection with
  the exact organization-approved server before any stdio startup. Built-in typo
  rejection stays; empty selection starts no servers. End-to-end config-to-runner
  tests must cover selected, denied, malformed and prefix-collision cases.
- Lock-timeout, duplicate/ambiguous completion, recovery/cancellation and issuance
  crash regressions remain mandatory. This approval establishes no test success
  or deployed closure.

## Human Work Item editing and transitions

Answered: user explicitly selected **Restricted edits (Recommended)**, option 1
below. Implement its field/state restrictions with atomic checks. Broader human
workflow was not selected.

Existing internal state setters accept any database enum
and serve scheduler/maintenance callers; they are not a safe public state machine.
The new GUI API therefore needs an explicit human-authority contract.

Options:

1. **Restricted edits (recommended)**: human edits of type, goal, acceptance,
   constraints and required gates only while open and not a maintenance proposal;
   human state changes only open-to-blocked and blocked-to-open, excluding
   maintenance proposals and agent-assigned items. Scheduler/review/completion
   states remain controlled by execution and evidence. Atomic expected-state
   checks prevent racing updates.
2. **Broader human workflow**: allow a broader human-managed lifecycle; the user
   must specify which additional states/edits are permitted and their required
   evidence. Never expose the unrestricted internal setter or bypass approval.

Revision-bound review enforcement proceeds independently as a correctness fix:
retain legacy unbound review history but require fresh, head-bound approval for
merge, including protection against a head change after the approval check.

## Short-lived credential provider for parallel closure

Answered: user selected **Specify another issuer**, then explicitly **OpenBao**.
Implement OpenBao dynamic-secret lease issuance, provider-enforced expiry and
revocation, with administrator-configured scoped roles and authenticated access.
Kubernetes TokenRequest is not the selected direction. Do not invent an endpoint,
role, authentication method or available engine; verify configuration before live use.

The credential lane found encrypted arbitrary static values,
not a configured issuing authority. Expiring a broker lease cannot expire the
underlying value. Selecting an issuing provider changes the supported credential
contract and requires approval.

Options:

1. **Kubernetes TokenRequest (recommended for Kubernetes access)**: implement
   administrator-configured org/environment/name-to-service-account and audience
   bindings, provider-reported expiry, and per-lease bound-object revocation.
   Verify actual revocation latency; do not promise immediate invalidation.
   This covers Kubernetes credentials only, not arbitrary external secrets.
2. **Specify another provider**: identify the intended Vault/STS or other issuer
   and supported target systems. Do not supply secrets in chat; configuration
   will use the established secret path.

Static secret storage must not masquerade as expiring credentials. OpenBao
selection authorizes the integration, not arbitrary privilege grants or disclosure
of stored credentials.

## Deployment executor and target authority for parallel closure

Answered: user selected **Existing delivery system**, then explicitly **Helm**.
Use administrator-approved releases/charts and preserve Helm ownership, scoped
human approval and durable execution evidence. Direct restricted Kubernetes
rollouts are not the selected direction. Do not accept arbitrary caller commands,
charts, targets or credential contexts.

Existing requirements define approvals, but the deployment
lane found no target/executor contract authorizing actual workload changes.
This is separate from approval to deploy NovaForge itself through Helm.

Options:

1. **Restricted Kubernetes rollouts (recommended)**: administrator-configured
   targets bound to org/repo/environment; change only an explicitly opted-in,
   non-Helm-owned Deployment's approved container image to an immutable digest.
   No caller-provided manifests, namespace, URL or shell command. Preserve
   human staging/production approvals, bind approval to target and digest,
   and record durable attempts and reconciled results.
2. **Specify an existing delivery system**: integrate the user's selected
   Helm/GitOps/CI executor with its ownership and approval contract instead;
   never patch resources owned by that system behind its back.

The Helm integration is approved. Live acceptance requires a configured,
explicitly authorized target and scoped credentials; this decision does not
permit mutation of unrelated releases or bypassing existing deployment gates.

## Performance-regression baseline policy

Answered 2026-09-16: **Option 1**, explicitly selected by the user. Compare
benchmark evidence from the latest eligible default-branch CI run with the
previous comparable successful default-branch run. Require matching benchmark
identity, units and execution-environment metadata. Missing or incomparable
evidence means unavailable, never zero or a guessed baseline.

A manually pinned baseline was not selected. Coverage's existing contract
compares successive evaluations and is unchanged. This records the chosen
policy, not completion of the still-missing production benchmark input.

## Commit and deploy verified completion-audit fixes

Answered 2026-09-16: **Yes**, to the recommended option. The user authorizes
committing and deploying each verified completion-audit batch to the existing
kw deployment without asking again for every batch. Review, tests and the
commit gate precede deployment; image preflight and Helm ownership remain
mandatory, followed by cluster acceptance.

Destructive changes and new product/security decisions still require separate
approval. The alternative of holding commits and deployments for per-batch
approval was not selected.

## SDK release for coding-plan support

Answered by direct user instruction: use `github.com/azrtydxb/go-ai-sdk v0.5.0`
(https://github.com/azrtydxb/go-ai-sdk/releases/tag/v0.5.0), including its support
for OpenAI and Anthropic coding plans. Parent owns the dependency pin and production
configuration; the runtime lane must validate against this release. Preserve the
SDK gateway boundary. A dependency upgrade is not evidence that coding-plan
credentials, routing, refresh, or deployed execution have been configured or tested.

## Closure supervisor contracts: durable authority and Work admission

Implementation coordination under the approved closure work:

- Deployment credential cleanup intent must be durable before issuance. Result-only
  persistence leaves a crash gap. Pending obligations prevent exclusion release;
  passive recovery can recover delivery evidence but cannot assert provider cleanup.
  Parent owns authenticated provider/janitor wiring; the deployment lane owns the
  exact-attempt fenced cleanup interface and persistence.
- Runtime lane now owns capability-store stable-ID issuance/cancellation methods,
  migrations and tests; parent retains authenticated owner RPCs and adapters.
- Work owner claims freeze execution intent and serialize with edits. Runtime stores
  the frozen protobuf bytes and durably retries claim/release. Only verified
  `agent-runtime` service identity may call these Work-owner RPCs.
- Release wire outcomes are `succeeded`, `failed`, `cancelled`, `over_budget`,
  `admission_failed`. Success moves to review, never approved/done; failures block.
  Admission failure can reopen only after no-execution/no-live-authority assurance.
- Absent-claim admission cancellation must durably tombstone exact org, Work item,
  repository, run and agent identity, serialized with claim. It changes no Work
  state; late claims fail. An unconfirmed release remains pending and blocks purge.

These contracts are not integration or deployed acceptance evidence.

## What happens next in the now-empty NovaForge repo

Answered 2026-09-11: **Nothing for now**, later superseded by explicit `/init`,
`/procoder:spec`, `/procoder:plan`, and `/procoder:todo` invocations, and finally by a
direct instruction to build the whole backend autonomously. Superseded — no longer open.

## Committing the 67 seeded task files, and the missing procoder templates

Answered 2026-09-11: **Commit the tasks and generate the templates.** Done in commit
e491a47, which added the 67 todo files, the procoder templates, and a copy of the pull
request template at `.github/PULL_REQUEST_TEMPLATE.md`.

## Build environment, after Docker Desktop was found broken

Answered 2026-09-11 by direct instruction: **use the kw cluster, and nexus rather than zot.**
Implemented in `hack/env.sh`:

- Images build on the in-cluster BuildKit at `tcp://192.168.10.130:1234` over mTLS, using the
  client certificate from the `buildkit-client-tls` secret. No local Docker daemon.
- Images push to the nexus registry at `192.168.10.131:5000` under the `novaforge/` prefix.
- PostgreSQL, Redis, and MinIO run in the `novaforge-dev` namespace, exposed as
  LoadBalancer services so the workstation can run integration tests against them.
