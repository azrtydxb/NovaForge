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

## NovaForge-authorized live OpenBao provisioning

Answered: user selected **Dedicated OpenBao (Recommended)**. Provision a
NovaForge-only instance via Helm with scoped roles and secret mounts.
Read-only inspection of `kw` namespace `novaforge` found
no OpenBao service or operator configuration ConfigMap. This does not establish
that no external issuer exists; none has been authorized for NovaForge here.
Another project's issuer/credentials remain out of bounds.

1. Provision a dedicated NovaForge-only OpenBao through Helm, with scoped roles,
   isolated acceptance targets and credentials mounted without disclosure.
2. Use an existing issuer whose endpoint, roles and credential mount the user
   explicitly authorizes; do not retrieve unrelated credentials.

This authorization does not establish outage-safe target expiry; prove it separately.

## Helm targets for live delivery acceptance

Answered: user selected **Isolated test releases (Recommended)**. Create
NovaForge-owned staging/production acceptance releases. This does not authorize
mutation of existing application releases.

1. Create isolated NovaForge-owned staging/production acceptance releases, proving
   both approval policies without touching existing application releases.
2. Use explicitly named existing application releases and their authorized scope.

Do not infer authority to mutate another Helm release from permission to deploy
NovaForge itself. Parent will continue implementation while these decisions resolve.

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

## Gateway execution reconciliation authority

Answered by structured question: **Extend gateway (Recommended)**. Scoped source
changes and isolated tests in `Fastllm-proxy` are authorized. Shared-gateway
deployment and unrelated route/account changes are not authorized.

Review admission must retain capacity while a timed-out
upstream model request may still be executing. The SDK v0.5.0 gateway adapter
exposes model/embedding calls, not durable execution status or acknowledged
termination. Inspection of the local `Fastllm-proxy` source found usage accounting,
but no demonstrated execution-reconciliation contract. Accounting events or a
cancelled HTTP connection are not proof of upstream termination.

Options:

1. Authorize a scoped extension in `/Users/pascal/Development/Fastllm-proxy` for
   durable authenticated request identity and observed execution completion,
   with a NovaForge owner adapter and isolated acceptance. This does not authorize
   deploying over the shared gateway or modifying unrelated routes/accounts.
   Provider outcomes that cannot be established must remain uncertain.
2. Supply an existing authorized gateway lifecycle/status API and its documented
   terminal guarantees for NovaForge to integrate and test instead.

Neither option weakens concurrency admission or treats timeout as completion.
Other NovaForge corrections continue alongside the authorized gateway work.

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

## Managed review consumer: independent-review runner failure

Answered via the structured question: **Native reviewer (Recommended)**.
The operator authorizes the native read-only reviewer within the governed
subagent workflow for this frozen consumer assessment, retaining all gates.

The consumer implementation is frozen in
`/Users/pascal/Development/NovaForge-integration-lanes/edge-reviews`, at baseline
`764c28320f0adf7976340937c9ca1ce8aa40f5ea`. Workflow
`cec50ddc-ff5a-4384-aef0-e8df7ed8777f` failed when review child
`07d02de0-78eb-4b8d-aee3-1c5ed7b52553` reported
`Codex error: The usage limit has been reached`. No review was produced.
Dirty state and failure are preserved in temporary capture
`nf-consumer-review-failure-xq2rxd87`; the approved native review may now launch.

Options presented:

1. Authorize the native read-only reviewer within the governed subagent workflow
   to assess the same frozen source and evidence, retaining independent review,
   canonical gates and parent-owned integration requirements.
2. Keep this review blocked until the original Codex runner's quota is available;
   continue unrelated closure work without accepting the consumer.

Neither option authorizes a shared gateway deployment or bypasses review.

## Gate sandbox Kubernetes authority boundary

Answered via the structured question: **Dedicated sandbox namespace
(Recommended)**. The operator authorizes option 1: pre-provisioned NovaForge-only
sandbox namespaces with namespace-scoped permissions, operator-managed network
denial and restricted pod admission. Do not grant Gates cluster-wide namespace
lifecycle authority. Startup received a scoped source/log review, but actual
sandbox execution remains unqualified. The current allocator creates a namespace
per tool invocation and requires cluster-wide namespace creation/deletion and
pod execution. Native RBAC cannot restrict those operations by an ownership label
or namespace prefix. The agent-runtime ClusterRole is not an acceptable shortcut.

Options:

1. **Dedicated sandbox namespace (recommended):** authorize changing Gates to
   run uniquely identified pods in pre-provisioned NovaForge-only sandbox
   namespaces, using namespace-scoped Roles, operator-managed deny-all networking
   and restricted pod admission. No namespace lifecycle authority, unrelated
   resource access or production credentials in tenant pods. Qualify isolation,
   bounded allocation and UID-bound termination/cleanup before deployment.
2. **Disposable acceptance cluster:** authorize a separate NovaForge-owned
   acceptance cluster to qualify the current per-invocation namespace allocator.
   Do not grant cluster-wide permissions on shared kw. This qualifies only the
   isolated acceptance environment; production authority remains unresolved.

Neither option declares the sandbox, image, cleanup or full platform complete.
The current shared deployment remains unchanged pending review and acceptance.

## Sandbox journal capacity — supervisor implementation contract

Parent implementation direction under the approved namespace redesign, **not a
new operator answer or permission to query another organization's tenant data**.
Recorded after supervisor request `66715bdd-e616-41bb-8803-371ba7605ef3` from
worker `89631b3c-f5bc-4d69-9d54-5d1bd94e5aa0`.

- A shared capacity row is operator-resource metadata only: immutable configured
  target/namespace, explicit ceiling and aggregate reservation count. No tenant
  identifiers, invocation identities, payloads or credentials belong in that row.
- Trusted startup construction supplies its configuration. Reservation callers
  cannot choose targets or reconfigure/widen limits. Existing configuration
  mismatch fails closed rather than changing configuration during admission.
- Counter changes and org-scoped invocation/fence changes are atomic. Exact
  retries cannot reserve twice; rollback cannot leak capacity. Unknown obligations
  retain their reservation until the specified settlement conditions hold.
- Every tenant invocation/fence query still has authenticated org predicates.
  There are no cross-org tenant scans/counts/joins to reconstruct the counter.
  Relevant resource-metadata queries must document this ownership distinction.
- Tests must cover concurrent reservations, foreign-org refusal, counter rollback,
  ceiling mismatch, idempotency and unknown obligations retaining capacity.

The child's unavailable finish-review tool is a verification limit. Parent owns
canonical gates and integration; no substitute tool or completion claim is allowed.

### Shared attempt and evaluation linkage

Parent approved supervisor request `160e0e55-65b4-4606-a920-c6b928b62c35` within
the worker's existing new-file/migration5 ownership, not as a new operator choice:

- Freeze org/attempt -> run, repository, gate, source, policy and snapshot, plus
  execution target/image profile where attempt-wide. Tool/command and pod identity
  remain invocation-specific, permitting multiple tools for one attempt.
- Freeze evaluation linkage to exactly one org/attempt and its immutable identity.
  Reject conflicting reuse atomically, including concurrent races. Identical
  retries remain idempotent; all tenant queries retain authenticated org predicates.
- Linkage reservation is not publication authority or proof that the whole attempt
  passed. Lifecycle settlement, captured tool results and final evidence acceptance
  remain distinct. Parent must verify the complete evidence set before publishing.
- No coupling to existing evaluation writers/cache/purge is authorized in this
  child slice. Integration remains parent-owned.
- Require real-PG tests for matching multi-tool invocations, immutable-field
  mismatches, evaluation reuse across attempts, concurrent conflicting registration,
  foreign-org refusal and rollback without capacity leakage.

## Sandbox stream completion dependency boundary

Answered via the structured question: **Qualify dependency correction
(Recommended)**. Isolated evaluation of a compatible upstream fix or minimal local
dependency patch is authorized, with regression and independent review retained.
Upstream publication and deployment are not authorized by this decision.

The isolated command-executor candidate has a real
local SPDY regression: remote stdout RST_STREAM followed by explicit Success is
accepted as EOF/success by `moby/spdystream@v0.5.1`. Its receive channels close
before the reset flag is published, so checking IsFinished after EOF races and
cannot prove complete output. Existing exported APIs expose no synchronized
normal-FIN versus reset receipt. No timing workaround or weaker test is accepted.

The six partial executor files remain unimported in the gate-isolation lane;
parent capture `nf-executor-reset-blocker-_07c9qcx` preserves them and verifies
all872 inherited paths unchanged. No deployment or dependency change occurred.

Options:

1. **Qualify dependency correction (recommended):** authorize isolated work to
   evaluate a compatible upstream correction or a minimal locally maintained
   dependency patch exposing reliable receive completion/reset errors. Preserve
   the regression, require deterministic race tests and independent review before
   parent adoption. This does not authorize upstream publication or deployment.
2. **Investigate alternate transport:** authorize a bounded comparison of supported
   Kubernetes exec transports and their actual end-to-end completion semantics
   before selecting another implementation. Do not assume changing transport
   fixes an upstream reset hidden by a proxy; retain the blocked candidate.

Both options retain the existing authority, output-integrity and canonical gates.
Neither authorizes accepting truncated output or activating the current executor.

## Opt-in stream lifecycle qualification scope

Answered via the structured question: **Qualify opt-in lifecycle (Recommended)**.
The operator authorizes the isolated wider qualification described in option1,
retaining deterministic concurrency tests and independent review. No dependency
adoption, upstream publication or deployment is authorized by this answer.

The strict-read experiment was rejected. It fixes
reset-as-success but breaks unchanged upstream and Kubernetes client tests.
Independent review confirms that preserving legacy reads with an optional receive
receipt is insufficient by itself: blocked DATA/HEADERS delivery and full partition
queues can prevent teardown. A larger, explicitly opt-in connection lifecycle and
adapter seam is necessary to prove bounded abort and joined worker completion.

Options:

1. **Qualify opt-in lifecycle (recommended):** authorize an isolated experimental
   extension for a strict operation-owned client: immutable receive receipts plus
   abort/join and adapter exposure. Constrain stream purposes, admission and
   callbacks; preserve legacy client behavior and all unchanged compatibility
   tests. Require deterministic saturation/cancellation/teardown tests and fresh
   independent review. No dependency adoption, publication or deployment yet.
2. **Hold sandbox and assess alternatives:** do not expand the dependency lifecycle
   patch. Keep the executor blocked and compare other supported execution
   transports/architectures with equally strict completion evidence before a new
   implementation decision.

This is a wider qualification scope than the rejected strict-reader patch, not
approval for a general networking rewrite or weaker completion guarantees.

## Live stream-completion probe

Answered via the structured question: **Run isolated kw probe (Recommended)**.
The operator authorizes only the temporary parent-owned transport probe and
bounded cleanup described below, not dependency adoption or Gates activation.
Local dependency corrections received scoped independent review; they are not adopted. Read-only discovery found kw v1.34.4+k3s1 with
containerd2.1.5-k3s1. Matching public runtime source writes status JSON and resets
streams without status FIN, suggesting the strict client cannot accept ordinary
successful execution. No dedicated sandbox namespace currently exists.

Options:

1. **Run isolated kw probe (recommended):** authorize parent-owned temporary test
   namespace/pod, denied network egress and no mounted service-account credentials,
   an explicitly selected immutable image, and fixed harmless commands only.
   Record real per-stream termination and bounded cleanup using captured resource
   UIDs. Do not execute repository/model commands, alter existing Helm resources,
   adopt the dependency, publish anything, or activate Gates. Report uncertain
   cleanup rather than widening deletion scope.
2. **Keep qualification local:** do not create cluster resources. Retain the
   interoperability blocker and use matching server-source/local protocol tests
   before deciding on any live probe or architecture change.

This probe would qualify transport behavior only, not the production sandbox,
its journal, security boundary, physical termination or platform acceptance.

## Status-message completion contract after the live probe

Answered via the structured question: **Qualify status-message profile (Recommended)**.
The operator approves the explicit single-terminal-message trust assumption for
isolated implementation and qualification of the separate opt-in profile below.
This does not authorize dependency adoption, publication or Gates activation.

The approved kw probe observed stdout/stderr FIN but status PeerReset for both exit0 and exit7. The current FIN-only profile correctly
rejects that path. Independent assessment `39626381-8b09-41a8-ad2c-35d2de1f518f`
finds an application-status profile conditionally feasible, not yet approved.

A complete validated status object cannot prove the absence of an unseen semantic
suffix or superseding status. The proposed profile therefore explicitly trusts a
qualified authenticated runtime and intermediary path to emit exactly one
immutable terminal status object, with no later semantic amendment. It must still
require both output FIN receipts, joined successful bounded copies, complete stdin
submission when present, strict case-sensitive status schema, preserved reset
codes, disqualifying transport/parser errors, cancellation fencing and joined
cleanup. Status reset remains reset, never status-stream byte completeness.

Options:

1. **Qualify status-message profile (recommended):** approve that explicit trust
   assumption for isolated implementation and adversarial qualification only.
   Keep the FIN-only profile/tests intact, add a separate opt-in profile and
   evidence identity, and require independent review plus authenticated live-path
   qualification. No adoption, publication or Gates activation yet.
2. **Keep FIN-only; assess server change:** retain mandatory status FIN and assess
   changing the server to emit it after a successful status write. No cluster-wide
   runtime upgrade or server change is authorized merely by choosing assessment.
3. **Keep FIN-only; assess another protocol:** retain the stronger terminal framing
   requirement and compare a different execution protocol; switching transports
   alone is not completion proof.

Neither valid JSON alone nor a successful Join establishes the proposed profile.
The receiver cannot defend this semantic guarantee against a compromised trusted
server/proxy; that limitation must remain explicit.

## Bounded reset and closure measurement

Answered via the structured question: **Run bounded closure measurement (Recommended)**.
The operator authorizes the bounded parent-owned follow-up below after classifier
review, without adoption, activation or changes to acceptance policy.

The first approved live probe measured only first stream receipts: it did not record reset codes, later control frames or actual
TLS closure. Its current SPDY reader stops on GOAWAY and then closes locally, so
its Join result cannot establish natural or authenticated peer closure.

A local Go1.27.1 TLS classifier experiment reproduces bare-FIN normalization and
co-delivered bytes+EOF behavior. Parent hash/mode verification and race20 rerun
are recorded; independent review is pending. No acceptance policy follows from
this experiment.

Options:

1. **Run bounded closure measurement (recommended):** after consuming classifier
   review, authorize a second parent-owned temporary kw probe with the same
   isolation, immutable image and fixed exit0/exit7 commands. Cap duration,
   received bytes and event count; record stream purpose, frame order/type,
   reset codes, GOAWAY fields and termination origin rather than arbitrary
   payloads or credentials. Continue observation beyond first receipts and
   GOAWAY; distinguish peer end from deadline/local abort. Join probe work and
   use UID-preconditioned cleanup. No executor acceptance, dependency adoption,
   runtime changes, existing Helm mutation or Gates activation.
2. **Keep closure work local:** create no further cluster resources. Retain
   unknown live reset/control/closure semantics and assess locally only.

Natural-close requirements, permitted terminal repeats/control frames, and
hostname/ServerName policy remain undecided regardless of the measurement result.

## Local framing/lifecycle qualification candidate

Answered by direct instruction in the structured question: **“stop asking me
questions, just assume the recommended answer by default.”** Apply the recommended
local-only candidate below. Use recommended choices within the authorized work
without repeatedly requesting confirmation; this does not itself authorize
adoption, deployment, activation or executor acceptance wiring excluded below.

This follows the second bounded measurement and read-only assessment
`fa065c4d-0279-4727-93bf-b6decae6fb08/closure-measurement-profile-assessment.md`.
The two traces observed status CANCEL=5, GOAWAY0 and classified boundary TLS EOF;
they do not approve an executor completion contract or prove remote exit/output.

Proposed authorization is limited to a fresh temporary local experiment:

- Require natural classified peer end for its positive candidate path. Deadline,
  local close, queue abort and exhausted bounds remain disqualifying observations.
- Initially reject duplicate/contradictory terminal events and post-terminal DATA
  or HEADERS, including reset-after-FIN; retain first receipts and later evidence.
  This is an experimental compatibility restriction, not deployed protocol policy.
- Qualify admitted-stream SYN_REPLY and bounded owned PING handling, dynamic
  GOAWAY coverage and read-through. GOAWAY is not mandatory or sufficient for end.
  Include WINDOW_UPDATE and four-stream/stdin boundary fixtures, but stop at a
  design/evidence checkpoint if correct credit/input handling needs broader work;
  unqualified traffic must not silently become eligible. No general SPDY rewrite.
- Use fixture-owned verified TLS only. Production authority/Host/base-path and
  certificate-name mapping remain deferred; no hostname-equals-ServerName rule.
- Add evidence APIs without changing legacy receipt layout, constructors, reader
  behavior, FIN-only tests or strict status parser. Drain and join successful
  intake; retain monotonic failures and qualify cancellation ownership.
- No cluster operations, real credentials, repository/dependency adoption,
  executor acceptance wiring, deployment or activation. Return an event/outcome
  matrix and independent review, not a completion/acceptance certificate.

Choices: **Authorize local candidate (recommended)** under these restrictions,
or **Keep design-only** and hold implementation until a different contract is specified.

## Where to start closing the code-vs-spec gaps (2026-09-25)

The analysis found no code-caused test failures: all 227 local failures trace to the
missing datastore host, the kw namespaces are gone, executable gates ship disabled
(`gates.analysisImage: ""`), and 41 files remain on five unmerged `integrate/*` lanes.
Ordered plan proposed as steps 1-8. Which work starts now?

- Restore the environment and switch on executable gates (steps 1-2) — redeploy the
  dev datastores and platform, build and digest-pin the gate analysis image, re-run the
  suites for a real baseline. Nothing can be verified before this.
- Integrate the unmerged lane work first (steps 3-4) — graph-producer's 28 files and
  three migrations, then the duplicated `identity_grants.go` and deployment-runner.
- Fix the security and spec defects first (steps 5-6) — `RecordProof` writable by any
  member, and the S-8 argument-level audit representation.
- Everything, in the proposed order 1-8, autonomously.

## How NovaForge should reach a dynamic credential provider (2026-09-25)

Correction: OpenBao IS deployed on kw, as `openbao-0` in the `sera` namespace,
initialized and unsealed (v2.4.1). An earlier note here inferred its absence from
the chart's empty `openbao.secretName`; that inference was wrong.

It still cannot be used as-is, for two reasons established by inspection:

- Its listener sets `tls_disable = true`, deliberately — the config records that
  it is in-cluster only and that adding a certificate would mean distributing a
  CA to its one client, Sera. NovaForge's broker refuses a non-loopback HTTP
  endpoint, because that hop carries credentials.
- The only OpenBao token in the cluster is `sera/sera-bao`, scoped to Sera's own
  KV mount. It cannot list mounts or auth methods, so it cannot configure a
  dynamic engine, policy or token for NovaForge.

Until one of these is resolved, `work_ci`'s brokered-secret job cannot run and
S-12's e2e step stays unproven on the cluster (S-12 itself is covered by Go tests
against a real OpenBao HTTP fixture).

- Deploy a separate OpenBao for NovaForge, with TLS, in its own namespace —
  leaves Sera untouched and gives NovaForge an provider it owns.
- Enable TLS on the existing sera OpenBao and issue NovaForge an admin-scoped
  token — reuses what is there, but changes another project's infrastructure
  against its recorded rationale.
- Supply an admin token and decide the TLS question yourself; I prepare
  everything else and apply it.
- Leave it. Record the prerequisite, accept work_ci failing, and move on.

## Open decisions after the closure run (2026-09-25)

State: 44/44 local packages pass, 12 of 13 in-cluster suites pass, traceability
33/33, deployed on kw, 25 commits, tree clean.

### 1. `integrate/gate-isolation`'s SPDY executor

`sandbox_executor*.go` (7 files) is a custom `v4.channel.k8s.io` transport. It is
not merged. BUILD-STATUS records its framing qualification, input/control
handling, authority binding and acceptance wiring as unresolved, and the isolation
problem it was research toward is solved and proven — the hostile-repository test
passes against the real sandbox.

- Leave it unmerged; delete the branch once its findings are recorded.
- Leave it unmerged but keep the branch indefinitely.
- Finish and qualify it now.

### 2. The FastLLM gateway

Its three proxy replicas rebuild backend registries independently (observed
7 → 9 → 10) and the configured MoE model intermittently exceeds the 120-second
upstream header timeout. One of three identical completions succeeded. This is a
separate project's infrastructure; `agent` is the only suite it blocks.

- Leave it; it is not NovaForge's, and the limitation is documented.
- Investigate and fix the gateway.
- Raise or remove the header timeout in the gateway's configuration.

### 3. OpenBao unseal material

`novaforge-bao/openbao-init` holds the single unseal key and the root token in a
Secret in the same namespace as the OpenBao it unseals, so anything that can read
that namespace's secrets can unseal and own it. That is convenient for a
self-hosted dev platform and wrong for anything else.

- Acceptable for this cluster; record it as a limitation.
- Move the unseal key and root token out of the cluster now.
- Re-key with more shares and a threshold above one.
