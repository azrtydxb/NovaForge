# Integration recovery checkpoint

The user renewed the all-gaps mandate after extension reload stopped workflow
`bb1fe4ad-315c-479b-8d80-70713ca05eca`. Its latest five child sessions were
**not resumable**. No older retained session was substituted for its newer state.

Parent captured current dirty bytes, modes, hashes and patches under
`/var/folders/s9/b7bq29kx4nvc5cp4x4jndd0h0000gn/T/novaforge-reload-recovery-j_k0p4g8`.
All existing lanes remain on `integrate/<lane>` at HEAD
`764c28320f0adf7976340937c9ca1ce8aa40f5ea`, with partial changes preserved.

One native fresh-context same-role replacement workflow,
`1546c157-0ca7-4f4b-bc78-a9d22d56a3ec`, is running six isolated writers:

| Lane           | Child run                            | Additional explicit ownership                                                      |
| -------------- | ------------------------------------ | ---------------------------------------------------------------------------------- |
| runtime-auth   | a4182c67-96ae-4678-be14-837b470a2699 | Existing runtime/Identity and real executor fixture scope                          |
| ci-governance  | c8e562e3-7022-4e35-ba72-68baa6f9f2ae | Strict gates resolver and source/target-bound MayMerge; not gates wiring           |
| graph-producer | 31d91b92-2eb7-42cb-a7c0-eac2db7da60e | knowledge/store.go and supersede_test.go, same-repository atomic supersession      |
| edge-reviews   | 207d6987-20fb-4f43-a116-ba012bf51d5d | Authoritative proof producer restrictions and persisted provenance                 |
| delivery       | 21875e84-ee11-42c2-8b1a-e5d13df07616 | Observed-success durable outbox, no inferred artifact/commit lineage               |
| gate-isolation | 84bdc2bf-892c-48a8-bc90-3d7463a60457 | analysis, gates/wiring.go, architecture.go, apicompat.go and focused sandbox tests |

The new gate-isolation worktree was seeded from the parent's combined checkpoint.
Its priority is L01: repository test/compiler code must not execute with gates
service authority. The discovered packages.Load path in architecture.go must also
cross the sandbox boundary; replacing only analysis.DefaultExec is insufficient.

Validation-only peer imports use the immutable reload snapshot with manifest hashes,
not live files being regenerated. Identity and Agents proto/generated dependencies
are approved for delivery validation and must be excluded from its implementation
ownership. All other transfers still require supervisor approval.

## Parent mount verification

New operator mounts in `deploy/helm/novaforge/templates/services.tpl` are scoped to
the actual consuming service, read-only directories rather than subPath, with
explicit disabled environment values and no secret bytes in Helm values.
Group-only Secret permissions include fsGroup 65532 for distroless consumers.
Unknown feature/field names and invalid path/Secret names fail chart rendering.

Observed regression failures:

- `/tmp/novaforge-operator-mount-red.log`: all five consuming mounts absent.
- `/tmp/novaforge-operator-mount-permission-red.log`: missing nonroot group access.
- `/tmp/novaforge-operator-mount-validation-red.log`: misspelled field accepted.

Subsequent `go test -race ./internal/service -count=1` and real `helm lint` returned
zero, with logs `/tmp/novaforge-operator-mount-final-race.log` and
`/tmp/novaforge-operator-mount-helm-lint.log`. This is local rendered-chart evidence,
not live mount, configured broker, or deployment acceptance. Independent review,
mutation checks, canonical gates and cluster verification remain open.

## Next bounded implementation and independent review wave

Gate isolation timed out at 1,800,000 ms after its initial checkpoint and while
working on offline advisory validation. Parent captured its complete current bytes,
modes and diff in
`/var/folders/s9/b7bq29kx4nvc5cp4x4jndd0h0000gn/T/novaforge-gate-timeout-epdks81f`.
Its worktree is `integrate/gate-isolation` at the same baseline HEAD. The latest
retained session was verified resumable; no external-mode fallback was used.

Workflow `c12686b2-469d-4a29-8d8c-bd626241073c` natively continues six retained
workers. Each successful bounded handoff is followed by a fresh read-only reviewer
inside this same workflow. Reviewers inspect source/evidence, not independent test
reruns. Scope of this iteration:

- gate isolation: finish already-started strict offline OSV manifest/scanner slice;
- CI: runner side-effect fencing races, immutable terminal state and full protocol fixtures;
  narrow transfer of retention/policy.go and focused tests now permits the owner
  log-deletion callback. The production callback must durably retry after object
  deletion, fence seal/append resurrection, and bind exact org/job/log generation.
  No retention defaults or evidence-retention semantics are changed by this transfer;
- runtime: durable tool events and bounded audit completion/reconciliation; ownership
  now explicitly includes tools/registry.go and focused audit tests;
- delivery: real secrets-owner adapter and separate Kubernetes materialization ledger;
- graph: observed deployment artifact event consumer with deletion/redelivery fencing;
- edge: cross-schema Git purge defect and Reviews state-update org predicate, followed
  by composed pinned-merge/proof tests where dependencies permit.

This sequencing does not waive the other audit leaves. Parent still owns configuration,
image/issuer provisioning, integration and canonical/deployed acceptance. No checkpoint
or reviewer may certify unavailable hard target expiry or whole-product completion.

## Received partial handoffs

Runtime-auth, CI/governance, edge/reviews and delivery returned bounded partial
handoffs under the workflow's `closure-recovery/` output directory. Parent read
all four reports and captured their complete owned deltas, including predecessor
work, under
`/var/folders/s9/b7bq29kx4nvc5cp4x4jndd0h0000gn/T/novaforge-four-checkpoints-rd83jrxy`.
The manifest records 39 runtime, 42 CI, 48 edge and 21 delivery paths with entry
and current hashes. Validation-only imports and generated web cache were excluded;
no duplicate ownership remained. Parent sources have NOT been replaced from this
snapshot, and these deltas have NOT received independent acceptance.

CI's inherited gates/wiring.go delta is only `TargetSHA: heads[target]`; it is
reserved for convergence with the isolation owner's changes, not imported twice.
Isolation additionally owns only the Input.PolicySHA field/comment in runner.go.
PolicySHA is the immutable target; legacy Input.TargetSHA remains source evaluation
identity. Missing target identity fails closed, including in direct-input tests.

Important retained failures: runner Redis/sealing side effects are not atomically
fenced with connection replacement; deployment credential materialization and
startup composition remain absent; Git purge test reads a missing cross-owner
capability table; browser tests lack the required browser space; durable tool-event
production and bounded audit retry remain open. Passing selected tests did not
close these findings. Gate isolation continues its approved analysis-only offline
advisory slice while freezing published gate files for later integration.

## Independent-review corrections required before activation

Gate review `ba32bf21-6798-425e-aac9-a4ef0c552564` returned BLOCK: embedding
bytes.Buffer promoted unbounded ReadFrom, and client-go can swallow writer errors
while reporting remote exit success. After the worker and reviewer stopped, parent
took sole ownership of the frozen gate sandbox files for this correction. New
io.Copy and stdout/stderr zero/nonzero-exit regressions failed for the reported
reason (`/tmp/nf-gate-output-red.log`). The current patch uses a private buffer and
sticky overflow checked before exit interpretation. Focused race tests and vet
returned zero; disabling the sticky check made the new regressions fail, with
immediate exact bytes/mode restoration and a subsequent focused race run.
Logs: `/tmp/nf-gate-output-{green,vet,sticky-mutation,restored-race}.log`.
Current lane sandbox.go SHA256 is
`a3aaf7f529cb81a703568648e7f22f361103bb1fcb2328673d8dadd352a3eccd`;
new sandbox_output_test.go is
`ae7e2cf137d97e7c202bc52814ee78443ccfa4331abecaca64cf98a2480ac7a9`.
This supersedes the frozen review snapshot for those files only. Independent
rereview, real transport/cluster acceptance and parent convergence remain pending.

Runtime reviewer also found required upgrade compatibility missing: legacy runs
with grants but no issuance receipt call RevokeGrant, which currently requires
StoredIntent. Migration must reconcile trusted persisted legacy org/run/subject
bindings without fabricating historical proof or granting a broad service bypass.
Receipt NotFound cannot acknowledge revocation. This belongs to the mandatory
capability schema/fence migration below.

Graph reviewer confirmed evidence-ID binding must survive repository purge via a
minimal org/evidence/original-repository identity tombstone. Exact replay to deleted
A is already fenced, but altered same identity targeting B must also be refused.
No payload, credentials or invented artifact/commit lineage should be retained.

Delivery review `a4d89556-c8be-42d4-95f0-9da1e4b0fcdb` accepted only the bounded,
currently fail-closed materialization increment with notes. Its P1 finding blocks
production activation: resolver reads grant expiry but the operation/attempt loses
that ceiling, while materialization requests attempt start plus ten minutes and
refuses shorter returned lifetimes. This violates the already-approved earliest
applicable grant/run/request ceiling. Propagate that ceiling into immutable durable
intent, cap the execution window, and refuse insufficient lifetime rather than
extend authority. Add short-grant/run, restart and cleanup replay regressions.
The real Broker still refuses unqualified hard expiry; no deployed exploit was
claimed. The reviewer inspected logs, not independently rerun tests.

Edge reviewer identified a concurrency-admission concern in the inherited review
queue: expired running attempts become uncertain, but admission counts only running
rows. Supervisor confirmed MaxConcurrentRequests bounds outstanding execution, not
only unexpired leases. Unknown potentially-active attempts retain their admission
obligation until execution closure is established; accounting availability is a
separate question. Do not release capacity merely by changing state, replay uncertain
model work automatically, or treat local timeout as remote process-death proof.
A correction must include durable reconciliation and an expired-A/still-active,
queued-B admission regression. Bounded Reviews org-predicate changes are assessed
separately from this predecessor queue defect.

## Capability schema and organization deletion blocker

Edge's follow-up source inspection found that Identity's capability owner RPCs
still use `gitplatform` grant/issuance tables in capability/grant.go and
capability/issuance.go. RPC ownership alone has not established schema isolation.
Identity.DeleteOrg also publishes before deletion without a durable capability
fence/deletion outbox. Removing Git purge's capability SQL alone would silently
omit revocation, so that shortcut is explicitly prohibited.

The current edge invocation is limited to its tested Reviews organization predicate
and composed pinned-merge/proof acceptance; no capability/Identity transfer is active.
The dedicated next correction must inventory and migrate preserved grant IDs,
issuance/cancellation tombstones and cleanup receipts into the Identity-owned schema,
with a rollout/backfill and compatibility plan. All issuance and resolution paths
must enforce the durable organization fence before deletion announcement. A
transition saga, if needed, must preserve retries and fail closed on partial work;
no cross-schema transaction or search-path workaround may disguise ownership.
Git purge remains an explicit failing acceptance obligation until that replacement
is implemented and tested. This requirement has not been waived or marked closed.

All 46 leaves in `completion-proof-audit.md` remain subject to actual closure
verification. No historical task status, backlog, milestone, commit or deployment
has been changed to imply completion.

## Independent review correction wave

Workflow `c12686b2-469d-4a29-8d8c-bd626241073c` returned all twelve children.
Its reviews do not authorize integration or release. Correction workflow
`b7070f6e-eb17-48c2-9c8d-f75cf75abd29` now assigns five isolated writers,
followed by fresh read-only reviews, and a separate sandbox rereview:

- CI: explicit final-sequence presence, ambiguous artifact-commit recovery,
  legacy seal acknowledgement, interrupted-run rollup, and stale local handshake
  registration. Required SHA-pair callers remain a parent convergence obligation.
- Runtime: bounded Redis publication without starving audit completion, durable
  partial publication progress, error classification, cleanup lease release, and
  authenticated legacy grant reconciliation. Schema migration and organization
  deletion require an explicit owner contract before expanding implementation.
- Graph: post-purge evidence identity, honest outgoing semantic dependencies,
  nullable context attributes, and fair pending/fresh event consumption. Narrow
  ownership additionally includes `internal/ctxasm/assemble.go` and
  `internal/events/deletion.go`, plus their focused regressions.
- Edge/Reviews: durable admission through execution uncertainty, reconciliation,
  and authenticated SSE cursor forwarding/replay. Immutable runtime wire imports
  are validation dependencies, not edge implementation ownership.
- Delivery: immutable earliest applicable authority ceiling through resolution,
  persistence, materialization and execution. Insufficient lifetime must refuse
  execution; it cannot authorize extending credentials.
- Sandbox: independent source/log rereview of the parent's private bounded buffer
  and sticky overflow check. Logs `/tmp/nf-gate-output-{red,green,vet,sticky-mutation,restored-race}.log`
  record local regression evidence, not deployed transport acceptance.

Parent retains startup/image/chart/issuer composition, integration and canonical
release gates. The latest canonical `procoder test` still reports 23 failures;
`procoder lint` reports zero findings. Neither result establishes datastore-backed
or deployed acceptance. All remaining audit leaves stay open; no task was closed.

## Parent follow-up: strict configuration and sandbox cancellation

The parent-only L04 increment in `internal/repoconfig/config.go` now rejects
trailing YAML documents, empty/null roots, unknown gate fields, missing/null
required flags, unknown gate names and duplicate gate definitions. It preserves
explicit false and the existing MCP tool grammar. Regressions exercise `Load`
and require path-qualified errors with zero partial configuration; these use the
existing Git-reader double, not a deployed owner chain. Logs under `/tmp/`:
`nf-repoconfig-strict-red.log`, `nf-repoconfig-gate-shape-red.log`,
`nf-repoconfig-duplicate-red.log`, `nf-repoconfig-null-red.log` and
`nf-repoconfig-final-race.log`. EOF-check mutation failed with exact restoration.
The original-entry/current scoped review capture is
`/var/folders/s9/b7bq29kx4nvc5cp4x4jndd0h0000gn/T/nf-repoconfig-review-5kqfr3wj`.
That capture includes the final null-root correction. Fresh source/log review
returned OK with notes and no qualifying findings; tests were not independently
rerun. Parameter validation and project-required-gate consumption remain
outstanding; this does not close L04 or S-18.

Sandbox rereview `92058ee2-367c-4050-8724-788158fb2229` found a cancellation race:
client-go can return before its output-copy goroutines join. Parent reproduced
it under the race detector (`/tmp/nf-gate-output-cancel-red.log`) and moved
non-exit-error handling before any buffer access. The new asynchronous-output
regression and existing bounds tests passed with race detection, three runs,
and vet (`/tmp/nf-gate-output-cancel-green.log`). This supersedes the earlier
sandbox hashes. The second fresh source/log review found no qualifying issues;
it inspected the installed client-go stream completion/cancellation semantics.
Transport and integrated acceptance are still required.

The user authorized scoped Fastllm-proxy extension and isolated tests, not shared
gateway deployment or unrelated route/account changes. The clean source at
`f61e6ea8125857df35a8f103c37829d080da791e` has a separate worktree:
`/Users/pascal/Development/Fastllm-proxy-novaforge-executions`, branch
`novaforge/execution-receipts`. The isolated owner implementation is in progress;
ordinary proxy requests must retain the gateway's no-database-I/O contract.
Parent's single-attempt transport primitive has behavioral mutation evidence,
not production qualification. A final successful fallback response cannot prove
an earlier timed-out attempt stopped.

### Parent seam corrections awaiting fresh assessment

- CI's ordinary terminal settlement now joins interruption's run-row lock
  protocol and takes its aggregate in a later statement snapshot. The real
  PostgreSQL regression holds interruption after its last aggregate and permits
  a second runner's authenticated terminal receipt to finish or wait on the
  run lock. Before correction both jobs became terminal while the run remained
  running. `/tmp/nf-ci-rollup-race-red.log` records this behavioral failure;
  `/tmp/nf-ci-rollup-full-race.log` records CI, runner and retention race suites
  against a randomly owned database and real Redis/MinIO. Removing the run lock
  reproduced the defect; exact bytes/mode were restored and the regression
  passed again (`nf-ci-rollup-mutation-red.log`, `nf-ci-rollup-restored-race.log`).
- Runtime's authenticated ToolEvents preflight now sends the explicit
  `x-novaforge-stream-ready: 1` marker required by edge, rather than empty
  metadata. The real owner-RPC reconnect regression failed on the empty header
  and passed after correction with race detection; idle and initial-error
  regressions also passed (`/tmp/nf-runtime-ready-marker-{red,green}.log`). Full
  edge-to-runtime composition still requires integration acceptance.
- The frontend test entrypoint now invokes its SSE transport regression before
  browser tests. Previously `npm test` passed even with a deliberately broken
  transport test; it now fails that mutation. Exact test bytes/mode were restored;
  transport and rendered fixtures passed in existing Ego TaskSpace 16, followed
  by TypeScript checks (`/tmp/nf-web-transport-wiring-{red,mutation,green}.log`).
  These are controlled rendered fixtures, not deployed browser acceptance.
- A real SDK v0.5.0 HTTP capture from `reviewBounded` exposed a gateway schema
  mismatch before consumer activation: `OutputObject` with gateway's
  `NativeJSON=false` emits a forced synthetic function tool named `output` and
  `tool_choice`, not just plain text. System/user content is string-valued;
  `chat_template_kwargs` reaches the wire when configured under `gateway`.
  `/tmp/nf-review-gateway-request.json` contains only a synthetic fixture;
  `/tmp/nf-review-gateway-wire.log` records its decoded round trip. The gateway
  owner was instructed to qualify bounded forced-output-tool requests and
  completions, never execute tools, and refuse unsupported forms. Gateway
  compatibility is not yet accepted.

Frozen six-file correction capture:
`/var/folders/s9/b7bq29kx4nvc5cp4x4jndd0h0000gn/T/nf-parent-seam-corrections-8mir66cf`.
Fresh read-only assessments run in workflow
`1f377dc9-b983-479e-8f0d-180c92a434a9`; gateway implementation/review remains in
`c724790e-45a9-4d83-8dbe-35074a8c07ec`. No task, deployment or global acceptance
has been closed by these component results.

### Revision-pair integration and rolling-writer correction

Parent converged the reviewed SHA-pair protobufs, gate controller/store policy
binding, Reviews gate adapter/Merger and native Git final transaction. MCP now
resolves both references through authenticated Git RPCs and refuses a gate-owner
response for a different pair. Its new transport regression was observed red;
removing the response-pair guard failed again after the correction, followed by
exact restoration. Full real-owned-database Gates, Reviews and MCP race suites
passed; actual Git source/target CAS regressions passed. Full Git still exposes
`TestAgentBranchScopeEnforced` using an unqualified no-executor runtime fixture;
that failure is not excused by selected passing tests.

Integration capture before review:
`/var/folders/s9/b7bq29kx4nvc5cp4x4jndd0h0000gn/T/nf-pair-review-3oz0xo6g`.
The independent reviewer found a P1: the old three-column gate upsert could
replace a newly qualified failure with an old-policy pass while retaining the
new policy tag during rolling deployment. Parent reproduced that exact legacy
SQL against real PostgreSQL (`/tmp/nf-gate-policy-legacy-red.log`). New migration
`000004_policy_writer_fence` replaces the conflict identity with source plus
policy revision, refusing old upserts; separate policy results remain separate.
Unqualified cache lookups read only unqualified evidence. Downgrade refuses to
relax the fence while qualified evidence exists, under a table lock. Both up and
down guard mutations failed behaviorally, with immediate exact restoration.
`/tmp/nf-gate-policy-fence-full-race.log` records the restored Gates/Reviews/MCP
race suites; independent reassessment is still required. No historical applied
migration was rewritten.

The gate stack fixture now connects review admission to the actual Git owner,
submits the inspected source SHA, and explicitly requires another independent
review after a push. Policy approval alone cannot refresh an old code review.
The fixture corrections preserve—not relax—these new authority checks.

Project configuration also rejects unknown, empty and duplicate required-gate
names. `/tmp/nf-project-gate-names-{red,green}.log` records targeted failure then
package race success. Consuming project-required gates in the gate owner and
strict per-gate parameter validation remain open.

Gateway timeout `d7cac96e-9188-47e0-b12f-c52c5dc72827` was captured before native
recovery in `nf-gateway-timeout-af7bovqi` under the same temporary root. Recovery
preserved all 22 source paths. Read-only review found no P0/P1; its misleading
"Both reconcile" test comment was corrected to distinguish indefinite unknown
intent from terminal completion readback. Three real-PG/local-HTTP mutations
now fail on dispatch after lost intent acknowledgement, success without durable
completion, and duplicate redispatch; restored runs pass. Evidence remains in
Fastllm worktree `target/execution-evidence/seam-convergence/`. No shared gateway
was deployed. The authenticated NovaForge consumer runs in workflow
`cec50ddc-ff5a-4384-aef0-e8df7ed8777f`, including independent review afterward.

The runtime repository-metadata concern was a stale cross-owner snapshot:
current runtime already serializes its persisted repository ID. A new real
signed-owner-RPC test proves GetRun and ListRunsForWorkItem metadata and foreign
scope refusal, with an omitted-field mutation and exact restoration. Only
`internal/agents/repository_metadata_rpc_test.go` changed in that lane.

Latest parent canonical check reports 0 blocking hygiene findings; finish review
remains **BLOCKED (2 blockers)**, including 23 canonical test failures and open
planning evidence. No task, commit, deployment or whole-product acceptance is
claimed by the above bounded results.

### Subsequent owner convergence and project-policy slice

Policy-writer reassessment `03b0ffa6-efc8-42b4-9e5e-8aece88f87c6` returned
scoped OK with notes, no P0/P1. Its P2 obsolete store comments were corrected;
actual mixed-version deployment remains untested.

Imported 39 changed delivery/secrets-owner files with prior bytes and hashes in
`nf-delivery-owner-integration-xl1kbof9` under the temporary root above. Imported
additive runtime/Identity protobufs with prior bytes in
`nf-runtime-owner-wire-ocg86i7i`. The parent still lacked runtime repository
serialization: imported owner regression failed with empty repo_id through
GetRun and ListRunsForWorkItem, then passed with the reviewed serializer field.
Logs: `/tmp/nf-runtime-repo-parent-{red,green}.log`. Other runtime ownership and
startup convergence remain open.

Delivery race suite passed (116.419s) in
`/tmp/nf-delivery-owner-integrated-race.log`; Secrets had three short-fixture
expiry failures. The fixture incorrectly began credential lifetimes at fixture
construction, before setup and authorization probes. A delayed-issuance test
reproduced rejection, then per-issued-lease expiry corrected fixture semantics
without relaxing production expiry checks. Two minute-long fixture credentials
were shortened to 30s so issuance remains inside their caller's minute-long
budget. Full Secrets race suite then passed (22.376s):
`/tmp/nf-provider-fixture-final-race.log`. This is not real issuer qualification.
Parent `go build ./...`, scoped vet and diff checks passed before the next slice.

Imported reviewed strict Gates decoding after reproducing permissive-parent
failures: `/tmp/nf-gate-decoder-parent-{red,green}.log`. L03 now has actual merge
regressions for both an absent gate definition and an explicitly optional one:
project.yaml on the target requires tests; source removes the requirement and
fails tests. Both merged incorrectly before the correction. Shared strict
`repoconfig.LoadProject` now feeds the target-pinned gate union. Omitting that
union failed again, followed by exact byte/mode restoration. Logs:
`/tmp/nf-project-policy-{red,green,mutation,final-race}.log`. Restored full Gates,
RepoConfig, Reviews and MCP race suites passed. Two Git fixtures were corrected
to return actual gRPC NotFound, not an untyped string error; production still
fails closed on unclassified errors. Independent review and live acceptance of
this increment remain outstanding; per-gate parameter validation remains open.

Consumer implementation `ef7be78c-a7ee-4b69-8366-490182b24362` returned a frozen
nine-file checkpoint, not integrated. Its review child
`07d02de0-78eb-4b8d-aee3-1c5ed7b52553` failed before assessment with
`Codex error: The usage limit has been reached`, failing workflow `cec50ddc...`.
The edge-reviews worktree remains at baseline `764c28320f0adf7976340937c9ca1ce8aa40f5ea`;
tracked diff, untracked archive, branch/HEAD and exact failure were captured in
`nf-consumer-review-failure-xq2rxd87`. No fallback or consumer acceptance is
implied. Parent-owned command/edge Gateway wiring and actual gateway/model
qualification remain required.

The operator approved a native read-only reviewer after that failure, recorded
in `.procoder/ask/decisions.md`. Workflow `68a15f80-16db-4205-932a-641e1e9b07bf`
returned scoped OK with notes, no findings, for the nine frozen consumer files.
Parent then added six wiring/test files in the edge-reviews lane: explicit
`execution_owner_url` startup/preflight and an authenticated TLS receipt fixture
replacing the unrestricted SDK model double in the real edge queue composition.
All nine reviewed consumer hashes and modes were verified unchanged. Snapshot:
`nf-review-parent-wiring-6jd7yg1w` under the temporary root above.

`/tmp/nf-review-owner-config-red.log` proves the missing operator field;
`/tmp/nf-review-owner-startup-mutation.log` rejects substituting the ordinary
inference URL, followed by restored race success. Queue composition passes with
real owner RPCs, PostgreSQL, Git, actual SDK and controlled authenticated TLS;
wrong terminal digest fails behaviorally, then restored race passes in
`/tmp/nf-review-queue-managed-restored.log`. Full Reviews/cmd race passes in
`/tmp/nf-review-owner-wiring-race.log`; scoped vet passes. This still does not
qualify the actual Fastllm/model or deployment.

Independent reviews of this startup/fixture increment and parent L03/L04 source
are running in workflow `8417d9a3-fa47-420c-92fc-46a734f389dc`; consume its results
before integrating or claiming those increments accepted. Latest finish review
remains **BLOCKED (2 blockers)** and now reports **46 canonical test failures**,
with hygiene/lint/security having zero blocking findings. Canonical failures
and planning signals are not cleared by the selected passing suites.

The project-policy review returned scoped OK with notes (child `39a6746b...`).
The startup reviewer `de9e4218...` found a P1 fixture-contract mismatch: response
bodies omitted integer `created`, and malformed function-argument JSON was
incorrectly certified terminal. A new response-shape regression reproduced both
violations (`/tmp/nf-queue-owner-shape-red.log`). The fixture now emits integer
`created` and owner-valid JSON-object arguments with an invalid application
verdict, retaining the consumer rejection/accounting case. The queue test also
compares approval evidence before/after rejection. Corrected owned-PG/SDK/TLS
race evidence: `/tmp/nf-queue-owner-final-race.log`. Reassessment remains required;
previous passing fixture evidence does not establish original-owner compatibility.

Parent also reproduced seven malformed architecture-constraint cases silently
passing (`/tmp/nf-architecture-params-red.log`). String-list extraction now errors
on null/scalar/mapping/mixed entries instead of dropping constraints; rule parsing
rejects empty sides and extra arrows. Focused race tests and full owned-PG Gates
race pass (`/tmp/nf-architecture-params-{green,full-race}.log`). This is runner-side
hardening only; comprehensive configuration parameter validation remained open
at that checkpoint. Workflow `8c13f320-4d8a-4a80-86d1-e536bae11b66` subsequently
returned scoped OK with notes for both the architecture hardening and corrected
owner-valid queue fixture, with no findings. These were source/log assessments.

The next parent increment adds shared `internal/gateconfig.ValidateParameters`
to both Gates resolution and RepoConfig gate loading. Unknown/cross-gate keys,
wrong types, malformed architecture rules, nonfinite/out-of-range coverage and
fractional/overflowing documentation counts fail closed. Both real loader entry
points were observed accepting four invalid policies before wiring the shared
validator (`/tmp/nf-shared-gate-params-red.log`). Full owned-PG Gates, RepoConfig
and contract-unit race suites pass in `/tmp/nf-shared-gate-params-green.log`.
Removing each loader's validation independently caused behavioral failure;
bytes/modes were restored immediately, followed by targeted race/vet/diff checks
(`/tmp/nf-shared-gate-params-{gate-mutation,config-mutation,restored}.log`).
Independent reviewer `b942c5ff-4dc7-46a6-88a4-e00310876068` returned scoped OK
with notes, no findings, for the shared validator and both loader guards.
Deployed acceptance remains open.

### CI and runtime owner integration

Imported 42 reviewed CI-owned changes (CI service/proto, runner connection and
retention policy), preserving prior parent bytes in
`nf-ci-owner-convergence-klmllzb1` under the temporary root. Compile, full owned-PG
race suites and whole-tree build passed: `/tmp/nf-ci-owner-convergence-{compile,race,build}.log`.
CI took 120.845s, runner 5.132s and retention 3.388s. This includes the previously
reviewed interruption/terminal rollup correction, durable connection fences,
artifact uncertainty and credential-cleanup changes, not a deployed assertion.

Imported 47 reviewed runtime-owner changes across agents, identity, capability,
agentrun, platformtest and tools; prior bytes/hashes are preserved in
`nf-runtime-owner-convergence-pmqo57dr`. Existing parent repository metadata
serialization was preserved by the identical owner implementation. Full owned-PG
race suites pass for all six packages, plus whole-tree build:
`/tmp/nf-runtime-owner-convergence-{compile,race,build}.log`. Capability physical
schema ownership/deletion fencing and command-level startup convergence are still
separate unresolved acceptance items.

The historical full-Git failure was reproduced after convergence: default
platformtest Start correctly refused nil-executor admission. Its branch-scope
test now uses the reviewed real Runner with controlled inference and actual
Work/Identity owners, waits at the inference barrier and settles on cleanup.
No production admission check was relaxed. Full Git race suite passes (30.466s),
including HTTPS/SSH/API scope and native CAS tests:
`/tmp/nf-git-qualified-runner-{red,green}.log`.

Canonical tool environment inspection showed TEST_DATABASE_URL/TEST_REDIS_URL
absent. Deployment tests deliberately fail rather than skip without PostgreSQL
(`internal/deployment/service_test.go:55-57`), explaining at least that class of
canonical failures; do not silently relabel the entire 46-failure report as
resolved or cached. Owned-environment runs above are separate evidence. The
canonical environment and remaining failures still require reconciliation.

### Review, Edge and sandbox integration follow-up

Imported 49 review/Edge changes and selected sandbox/analysis dependencies;
backups are `nf-review-edge-convergence-1eogncij` and
`nf-sandbox-owner-convergence-y1mpx5xf`. Parent policy-pair/writer fencing,
strict parameter validation and Git NotFound fixtures were preserved. Initial
combined races failed; `/tmp/nf-sandbox-review-edge-integration-race.log` is
negative evidence, not acceptance.

Edge integration exposed missing owner seams: logout only cleared a cookie;
review admission had no Git owner in platformtest; stream clients and the runtime
server lacked streaming credential interceptors. Logout now authenticates the
selected credential and requires Identity's revocation acknowledgement before
clearing its cookie. Runtime startup now installs stream authentication alongside
unary authentication. The GUI event fixture uses the real controlled Runner and
durable audit events with valid terminal outcomes, not Redis-only fabricated
notifications. Generated OpenAPI was refreshed from mounted routes.

Selected logout and stream regressions were observed failing before correction.
Final full owned-database races passed: Edge 80.396s, Reviews 38.032s and
work-reviews command 4.829s (`/tmp/nf-edge-reviews-integrated-final-race.log`).
Scoped vet/diff checks passed. These increments await independent review and
command/deployed acceptance; platformtest is not a Kubernetes runtime proof.

The staged offline OSV fixture hash was independently verified against the owner
report (`7c33aee9006c6e2564a4087b7b459ad5d54059b66fcddcc00e86b043490fe439`).
Analysis race suite passed with explicit `NF_TEST_OSV_GO_ZIP`
(`/tmp/nf-analysis-owner-convergence-race.log`, 13.408s). Gates stack tests still
need authenticated proof-production and qualified sandbox execution fixtures;
architecture component tests still omit the now-required executor. No production
sandbox fallback has been introduced. Canonical completion remains blocked.

### Independent Edge assessment and scoped proof authentication

Reviewer `9e881ec5-232e-430b-891b-85516310503b` returned scoped OK with notes
(source/log only). Its P2 finding was a stale SSE summary claiming no replay.
The route now describes durable cursor-based tool-event replay; OpenAPI was
regenerated and route coverage passed. No new cluster or process acceptance is
inferred from the review.

The Gates proof failure was reproduced in `/tmp/nf-gates-proof-owner-red.log`.
`WithProofService` now derives a one-minute, org-scoped gates credential only
for proof publication. It removes incoming/outgoing caller credentials from the
derived proof context, preventing the shared forwarding interceptor from adding
a second bearer, while leaving caller reads and their original context unchanged.
Command startup, platformtest and the Gates stack use this common option.

A new approval-only real-owner stack test verifies authenticated proof provenance
and a native merge without exercising executable gates. Scoped tests pass after
two behavioral mutations (unsigned proof and duplicate forwarded credential) were
rejected and exact source bytes/modes restored:
`/tmp/nf-gates-proof-{scoped-green,unsigned-mutation,duplicate-mutation,restored}.log`.
The architecture component fixtures now explicitly provide the real local executor
for their test-authored sources; this is not a production fallback or isolation
proof (`/tmp/nf-gates-components-qualified.log`). Whole-tree build and scoped vet
passed with recorded exit codes in `/tmp/nf-proof-integration-{build,vet}.log`.
Reviewer `35d4bccc-5775-437e-a94c-26ac5f43b76b` subsequently returned scoped
OK with notes/no issues for these proof increments, including the SSE description
correction. It assessed source and supplied logs, not rerun tests or deployment.

`/tmp/nf-gates-proof-owner-green.log` is **negative despite its name**: after proof
wiring, executable-gate paths still lack a sandbox. The final full Gates race run
also fails four top-level tests for that reason
(`/tmp/nf-gates-after-proof-integration.log`, 33.914s). Do not replace their executor
with a permissive result or claim the focused approval fixture proves isolation.
Production sandbox configuration/image qualification also remains outstanding.

Latest canonical `procoder check` reports 52 failing tests, zero blocking hygiene
findings, and zero unformatted/unchecked files. Full report:
`/var/folders/s9/b7bq29kx4nvc5cp4x4jndd0h0000gn/T/procoder-1789676951507.txt`.
This supersedes the previous 46-failure count, not the blocked completion verdict.

### Gate sandbox operator startup (partial qualification)

The Gates command now reads `NF_GATE_ANALYSIS_IMAGE` from the common service
configuration, loads only in-cluster Kubernetes credentials when explicitly
configured, constructs the digest-pinned sandbox and passes it to the controller.
Invalid configuration refuses startup before database migrations; absence logs
that executable gates are unavailable and obtains no Kubernetes credentials.
Helm renders the image only for Gates from `services.gates.analysisImage` and
rejects mutable references. Defaults remain empty. No RBAC authority was granted.

Initial configuration tests failed behaviorally against the no-sandbox state
(`/tmp/nf-gate-sandbox-startup-red.log`). The first full service run then exposed
missing chart wiring and a missing exhaustive-config fixture variable;
`/tmp/nf-gate-sandbox-startup-green.log` is therefore **negative**. Final races
pass for cmd/gates and service (2.180s and 9.523s):
`/tmp/nf-gate-sandbox-startup-restored.log`. Removing the configured image from
Helm and substituting an immutable constant for invalid operator input each made
focused tests fail; exact bytes/modes were restored
(`/tmp/nf-gate-analysis-{chart,image}-mutation.log`).

This qualifies configuration construction and Helm rendering only. A running
command, actual sandbox image contents, restricted RBAC, real Kubernetes
execution/network denial/termination and the four stack tests remain unproven.
The image setting is not a grant to reuse the runtime's broad cluster role.
Startup reviewer `8d53266a-2a09-4f0d-a350-ef82bb104a37` returned scoped OK
with notes/no issues, source/log assessment only. Canonical completion remains
blocked.

### Approved namespace-scoped sandbox redesign

The operator selected **Dedicated sandbox namespace** in the structured question
recorded in `.procoder/ask/decisions.md`. The qualification scout
`62523132-1837-4214-b9f4-5d54b639f13c` identified that native RBAC cannot enforce
ownership labels/prefixes on the current dynamic namespace allocator. Do not add
a cluster-wide Gates role or enable the existing broad `services.gates.rbac` role.

Next implementation scope, not yet implemented:

- Require an operator-specified, pre-provisioned sandbox namespace together with
  the image. Repository inputs cannot select either. Give each invocation a unique
  pod name; bind readiness, exec targets and cleanup to that invocation.
- Remove namespace and NetworkPolicy mutation from sandbox execution. Dedicated
  namespace-scoped Roles admit only pod create/get/delete and exec POST. Policies,
  restricted pod admission, service-account identity and resource quotas are
  operator/Helm-owned, not mutable by Gates. No secrets or unrelated workloads may
  reside in the sandbox namespace; do not reuse the application namespace.
- Preserve terminal-container and exact UID checks before pod cleanup; retain and
  report uncertain creation/termination obligations. Never delete the shared
  namespace or interpret a missing pod/timeout as proven execution completion.
- Bound pod allocations with operator-enforced quotas. Define owned-resource
  reconciliation and crash-recovery evidence before claiming lifecycle closure.
- Add behavioral negative tests for cross-namespace/lifecycle/policy operations,
  concurrent invocation identity, and unknown/replaced pod cleanup. Preserve the
  four existing real-owner stack assertions. Use owned reachable canaries for
  network qualification, not unrelated datastore or public endpoints.

Image contents/pullability, scoped authority and actual Kubernetes execution still
require qualification. No shared-cluster mutations have been authorized merely
by configuration tests, and no deployment has changed.

### Journal store review correction (not integrated)

Namespace design review `66bd1cf3-c0b0-4540-bdff-ecdb24ab437b` blocked activation
on five lifecycle/identity/provisioning/capacity contracts. These are specified in
`gate-sandbox-lifecycle.md`. The chart now rejects the generic broad Gates RBAC
switch; its regression was observed red and the service race suite passed
(`/tmp/nf-gate-broad-rbac-{red,service-race}.log`). This does not provision a sandbox.

Worker `89631b3c-f5bc-4d69-9d54-5d1bd94e5aa0` added twelve journal/migration/test
files in the isolated gate-isolation lane. Reviewer
`6a29dd03-e988-464d-b1a3-1ce68c7c22bd` blocked acceptance on lossy command JSON
encoding: invalid UTF-8 argv values produced identical hashes. Parent reproduced
that collision in `/tmp/nf-journal-utf8-red.log`, then added UTF-8 validation with
valid-Unicode and byte-distinction regressions. A related real-PG regression found
invalid tool text consumed capacity while its persisted identity changed; this is
recorded in `/tmp/nf-journal-text-identity-red.log`. Gate/tool text now also rejects
invalid UTF-8 before reservation.

All fifteen focused journal tests passed against a randomly owned PostgreSQL
database under race detection (`/tmp/nf-journal-utf8-final-race.log`, 17.644s).
Removing the command UTF-8 guard failed behaviorally; exact bytes/modes were
restored and the focused test passed
(`/tmp/nf-journal-utf8-{mutation,restored}.log`). Scoped vet has an explicit exit
code in `/tmp/nf-journal-utf8-vet.log`.

Parent verified all 851 inherited lane paths against their original hashes/modes.
The correction now spans thirteen journal paths, including one added text-identity
regression file. Capture `nf-journal-utf8-correction-s5gvoyc8` contains final hashes
and an exact correction diff; reconstructed pre-correction source hashes match
the worker report. The journal remains in the lane pending correction review and
parent integration, not deployed or called by the executor.

Lost-acknowledgement tests discard successful responses/reopen the store; they are
not real lost-COMMIT transport tests. Executor/recovery/purge/publication wiring,
namespace security provisioning, real-cluster qualification and canonical finish
remain unresolved.

### Parent journal integration and synchronous acknowledgement correction

UTF-8 correction reviewer `a4be14f3-b592-4661-94cd-0f10c4e4cc2e` returned scoped
OK with notes/no issues. Parent imported all thirteen journal paths after checking
the final hashes/modes and refusing any existing-path collision. Capture
`nf-journal-parent-import-26be7vch` records the import manifest and prior status;
no parent file was overwritten. Focused owned-PG races passed against the parent's
complete Gates migration chain (`/tmp/nf-journal-parent-integration-race.log`).

A new real-PG regression set connection defaults to `synchronous_commit=off` and
used deferred constraint triggers to check the effective setting at commit.
The imported constructor failed that test
(`/tmp/nf-journal-durability-red.log`). Journal transactions now explicitly set
`SET LOCAL synchronous_commit=on`; operator metadata installation also belongs
inside that transaction. Reservation, lifecycle/linkage updates and deletion
fences use the same helper. Pooled connection defaults remain unchanged.

Independently bypassing durability in constructor, reservation, transition and
fence paths caused the commit-time assertion to fail. Each mutation was followed
by exact bytes/mode restoration. Final owned-PG races include sixteen journal
tests plus all three policy writer-fence regressions (19.398s):
`/tmp/nf-journal-durability-restored.log`; mutation logs are
`/tmp/nf-journal-durability-{constructor,reserve,transition,fence}-mutation.log`.
Whole-tree build and scoped vet report explicit exit0 in
`/tmp/nf-journal-parent-build-vet.log`. This checks transaction settings, not
power-loss durability, actual lost-COMMIT responses or replica failover.

The parent finish-review checkpoint remains **BLOCKED (2 blockers)**: canonical
suite reports52 failures and generic planning signals remain unreconciled. Lint
reported zero findings; security reported seven informational, zero blocking
findings. Selected races do not override that verdict. New durability changes
received independent scoped OK/no issues from reviewer
`fc5d22ae-6f13-48e9-80b3-aa5608466256` (workflow
`afe381e6-a4b1-431d-8325-53c804febc9b`). That source/log assessment does not verify
import hashes or actual lost-COMMIT/power-loss behavior. Executor/recovery and
publication wiring and sandbox activation remain unfinished; deployment unchanged.

### Sandbox deletion-consumer integration in progress

The real Gates purger now invokes durable sandbox fences as well as credential
fences before deleting evaluations/approvals. Both owners are fenced even if one
reports pending obligations; errors remain retryable. The private fencing view
requires only the database pool, not a current execution target, so disabled or
retired target configurations cannot bypass outstanding obligations. Journal
receipts, reservations and tombstones are retained after successful purge.

Real owned-PG regressions first observed both deletion paths returning success
with unresolved sandbox obligations (`/tmp/nf-sandbox-purge-red.log`). Tests now
exercise `cleanup.Gates` event handlers, late execution refusal, credential
fencing despite sandbox uncertainty, evaluation retention, terminal-but-not-absent
refusal, eventual settlement, repeated purge, foreign-organization preservation
and obligations across two targets. Three independent mutations omitted run
fencing, organization fencing and credential fencing respectively; each was
rejected and exact bytes/modes restored. Final selected journal/policy/purge
races: `/tmp/nf-sandbox-purge-verified.log` (26.961s).

`/tmp/nf-sandbox-purge-final.log` is an earlier **negative**: the added retired-target
test incorrectly treated exact reservation readback as fresh admission. Its final
assertion now uses a new invocation/attempt identity; existing readback never
confers create/exec authority. The corrected regression passes in the verified
log above. Production admission checks were not weakened.

Full cleanup-package race remains negative in
`/tmp/nf-sandbox-purge-cleanup-race.log`: existing
`TestAgentRuntimeCancelsThenPurgesRuns` expects an unconfigured runtime purger to
erase running SQL-seeded runs, but receives `resource cleanup pending`. This
requires owner-lifecycle fixture reconciliation, not bypassing the guard. Purge
integration received scoped OK/no issues from reviewer
`40d39e2d-0c26-4b12-a129-19ff11ede14a`; no whole-suite or activation claim.

### Runtime purge fixture convergence

Worker `4e69b487-5930-424a-ac2d-2277b87de19c` replaced the impossible SQL-seeded
runtime fixture with API-created executions and the real Runner plus authenticated
Identity/Work owners. An explicit controlled-model return barrier distinguishes
cancellation from durable completion. Independent tests retain Work ownership
while execution is unfinished or an undispatched tool attempt has pending audit
evidence. Only an actual terminal audit receipt and acknowledged owner cleanup
permit successful purge. Original announcements, eventual deletion and isolation
assertions remain, with additional foreign-organization checks.

Reviewer `236cdf74-dc6a-403e-b927-7855d0975071` returned scoped OK/no issues for
the two test paths. Parent verified the reported hashes/modes, all866 unchanged
inherited paths (including temporary production mutation restoration), and bytes
outside the authorized existing test block. Two-path import capture:
`nf-runtime-purge-parent-import-vzajrcjb`. No production change was imported.
Parent full cleanup race passed all six tests without skips in17.896s:
`/tmp/nf-runtime-purge-parent-race.log`. Worker logs separately show both safety
mutations rejected; restored lane cleanup race passed in16.226s. The local Runner
helper and exact-restored production mutations were explicitly supervisor-approved.

This establishes controlled-model/real-Runner lifecycle and owner transport/storage
behavior, not execution of the attempted git.commit, provisioned Kubernetes
workspaces or physical termination. Canonical finish and sandbox executor/recovery/
publication integration remain outstanding; deployment is unchanged.

### Namespaced lifecycle candidate: reviewed parent import

The initial worker failed with `fetch failed`; five partial new files and exact
inherited preservation were captured in `nf-namespaced-fetch-failure-oifmzndw`.
The operator requested retry. Same-protocol resumed worker
`345f2c85-5d6a-4710-b222-fe23858cf552` delivered six new namespaced lifecycle
files; reviewer `b31d46a8-3492-4042-a0f9-0a56149a4d52` found no qualifying
in-scope defect. Parent verified866 unchanged inherited hashes/modes, unchanged
tracked patch and all six candidate hashes/modes before collision-free import.
Capture: `nf-namespaced-parent-import-yidq_2ti`.

Parent selected namespaced/journal/purge/policy races passed in38.736s:
`/tmp/nf-namespaced-parent-race.log`. Whole-tree build and scoped vet have explicit
exit0 records in `/tmp/nf-namespaced-parent-build-vet.log`. This supplements the
reviewer's warning that empty child build logs alone do not establish exit status.
Creation disables client-go retries and redirects; local HTTP/fake/owned-PG tests
are not actual Kubernetes admission, isolation or end-to-end exactly-once evidence.
Old allocator/startup/controller wiring remains untouched. Executor integration,
recovery enumeration, publication and activation remain outstanding.

### Owned full-Go diagnostic and fixture/traceability follow-up

`/tmp/nf-parent-owned-full-go.log` records a negative full Go run on a random
owned database with the explicitly hash-verified staged OSV fixture. Ten top-level
tests failed across six packages: Git-platform branch locking, agent issuance
recovery, CLI lifecycle, four Gates stack tests, two maintenance tests and spec
traceability. This is a separate environment-qualified diagnostic, not a change
to the latest canonical52-failure verdict.

Traceability cited deleted `TestArgsAreStoredVerbatim`; it now cites the actual
metadata test and honestly marks S-8 partial because metadata is not argument
values. `/tmp/nf-traceability-current.log` passes structural reference checking
with32 covered/1 partial; this is not semantic acceptance. BUILD-STATUS and the
proof audit record the unresolved privacy-safe argument-evidence contract.

Maintenance fixtures now share the existing explicit offline advisory staging
helper through `internal/analysistest`, mapping host test paths only, preserving
real scanners, findings and assertions. Analysis/maintenance owned races passed
in12.547s/21.753s (`/tmp/nf-offline-fixture-convergence.log`). These parent fixture
changes still need independent review; test-generated manifest validity windows
are not production advisory freshness evidence. No production fallback added.
Reviewer `2fb34ceb-265d-423b-a09d-4af444732aae` subsequently returned scoped
OK/no issues for these fixture and traceability changes (source/log assessment).

### Executor transport blocker and authorized dependency qualification

The isolated executor candidate is **not accepted or imported**. Real local SPDY
stdout reset followed by explicit Success status is reported as EOF by installed
`moby/spdystream@v0.5.1`, allowing incomplete output to appear complete. The
library closes receive channels before publishing its reset flag; checking that
flag after EOF is racy. Worker stopped rather than weaken the regression.
Capture `nf-executor-reset-blocker-_07c9qcx` preserves six new candidate files;
parent verified all872 inherited paths and original tracked patch unchanged.

The operator selected **Qualify dependency correction (Recommended)** in the
structured question, recorded in decisions.md. Temporary-source/alternate-modfile
qualification is proceeding without dependency adoption, publication or deployment.
It must expose synchronized receive termination, preserve valid FIN/half-close
behavior, and reject reset/disconnect without timing heuristics. Remaining executor
journal/failure/image tests are not yet implemented; lifecycle component review
must not be represented as executor acceptance.

### Recovery test isolation correction under review

The owned full-Go run exposed a fixture interference seam: issuance recovery is
correctly platform-wide, but its test used a shared agents database with a
fixture-local Work owner. Concurrent packages' unresolved runs produced legitimate
cleanup errors and could be visited by the wrong fixture. Only
`TestIssuanceRecoveryAndLateReplyAreFenced` now uses the existing newly owned
repositoryMetadataPool helper for agents rows. Recovery, lease-expiry, late
issuance/claim refusal and branch-lock assertions remain unchanged; no production
error is ignored and no production code changed.

Reverting the fixture correction reproduced `resource cleanup pending` in that
test while cleanup/edge ran concurrently; exact bytes/mode were restored:
`/tmp/nf-agent-recovery-isolation-reverted.log`. Restored three-package races passed
agents34.558s, cleanup16.210s, edge74.013s in
`/tmp/nf-agent-recovery-isolation-restored.log`. This is test isolation, not a new
production recovery guarantee. Reviewer `3b9c0a6f-19f5-468e-ad00-123e81948dff`
returned scoped OK/no issues. It also noted that the reverted log includes
`TestTerminalGrantCleanupRetriesWithoutRerunning` failing on the same foreign run;
the restored three-package run passed both tests. The agents database is isolated;
existing capability/Redis helpers and the Work protocol double remain unchanged.

### Strict SPDY dependency experiment rejected for global adoption

Worker `234a1e5c-8b73-4d15-9b91-34dc8ea8dbdb` qualified a temporary-source
strict-read experiment against verified `moby/spdystream v0.5.1`; no newer released
or inspected master correction was found. Artifact root:
`nf-spdy-qualification.TGG9Ivinos`. Its synchronized receive reason rejects the
unchanged local executor reset-as-success regression, including repeated races.
However, three unchanged upstream EOF-contract tests and Kubernetes streaming/
remotecommand tests regress. This is patch-induced incompatibility, not a green
qualification or evidence that a live API server always uses those conventions.

The experiment is **rejected for process-wide replacement**. Cancel/Refuse still
lack qualified local receive semantics; blocked partition workers can delay
connection-loss notification. No dependency/source/modfile adoption occurred.
The only additional lane file is the authorized dependency wire regression;
all878 pre-experiment paths were reported hash/mode-preserved. Parent has not
imported any executor or dependency candidate. Child gate output is not parent
canonical clearance. An opt-in completion receipt preserving legacy readers
requires separate scoped design and compatibility review before implementation.
Reviewer `bbda2884-2c6d-4bff-acdc-8153f54f5bea` confirmed rejection: additive
receipts alone cannot bound teardown, because unread DATA/HEADERS or a saturated
partition queue can block worker drain. Kubernetes wrappers also hide optional
capabilities. A proposed strict operation-owned abort/join seam is design work,
not an implemented guarantee; broader scope needs explicit authorization.

### Branch-lock fixture parent integration and CLI diagnostic

Reviewer `3910a0ea-ebf3-48b6-b476-bd8140b39b0d` returned scoped OK for the
branch-lock fixture. Parent verified867 unchanged inherited paths, two candidate
hashes/modes and original parent baseline before importing only the test/helper.
Capture: `nf-branchlock-parent-import-bxvkha64`. Three complete package race
repetitions passed in20.366s (`/tmp/nf-branchlock-parent-race.log`). Production
branch guards are unchanged. This exercises smart HTTP (not TLS) and SSH; the
CancelRun call is idempotent after controlled Runner settlement, not first-call
active cancellation or pending-cancellation lock retention evidence.

The CLI lifecycle fixture now starts and joins a controlled real Runner rather
than attempting nil-executor admission. Its existing strict merge assertion now
reaches the unresolved documentation sandbox boundary and still fails:
`/tmp/nf-cli-controlled-admission.log`. No assertion was weakened or permissive
sandbox installed. This partial fixture edit awaits review and cannot establish
CLI lifecycle acceptance until sandbox integration is qualified.

### Opt-in transport experiment: reviewed locally, server compatibility unresolved

The first opt-in worker timed out after1800000ms; parent captured155 experiment
files at `nf-spdy-optin-timeout-c1asukhp` and verified879 inherited lane files and
modes unchanged. Recovery produced a checkpoint, not acceptance. Independent
review `8c0df899-b2eb-40b1-9014-7e232df450f7` blocked it on alternate substream
admission, unjoined cancellation helper, receipt cause, retained output on cleanup
failure, deterministic test gaps and formatting. A fresh experiment at
`/tmp/nf-spdy-corrections.m3rfkq7t` contains corrections and test evidence.
Reviewer `b69ae96d-8625-4fe5-9c6d-45e087686194` returned scoped source/log OK with
notes; two exact cancellation-boundary tests remain a separate isolated slice.
No dependency or executor import/adoption follows from these reviews.

Parent read-only cluster discovery reports kw v1.34.4+k3s1 and all nodes running
containerd2.1.5-k3s1. Public matching runtime-tag source declares kubelet v0.34.0.
Captured source at `nf-server-fin-source-5mkobo74` shows ServeExec writing status
JSON and deferring connection Close; that Close resets streams, while the status
writer does not send FIN. This predicts a material incompatibility with mandatory
status FIN even for ordinary successful commands. Public tag source is not proof
of deployed binary identity or an observed wire trace. Before adopting the strict
client, qualify the real server path; do not reclassify reset as successful FIN to
obtain a positive canary. Sources, URLs and SHA256 values are in that capture's
`provenance.json`. That source-discovery step modified no cluster resource.

The operator subsequently authorized an isolated live probe. Parent capture:
`/tmp/nf-live-stream-probe.2WQhajpE`. Namespace
`nf-transport-probe-bb36873bf6` held only probe resources, with restricted Pod
Security admission, deny-all ingress/egress NetworkPolicy, quota, explicit
unmounted service account, nonroot read-only container and cached immutable
Nexus Git image `sha256:d79fb510a8e187825fb0ce29c90d14cfc34d0c4cc96ca227f37f62d9726b0c88`.
Only fixed printf/exit0 and printf/exit7 commands ran; no application was started.
Both produced stdout/stderr PeerFIN, complete status JSON, and status PeerReset.
This confirms the transport contract incompatibility on the observed kw path;
the diagnostic's PASS denotes receipt collection, NOT strict executor acceptance.
The probe never promoted these bytes to accepted output. Pod and namespace were
deleted with captured UID preconditions, acknowledged, then independently read as
NotFound. This does not prove production journal cleanup or physical termination.
No existing Helm resource, source dependency or deployed service changed.

Boundary-only review `24df93db-0601-43d3-a4cc-d59708d169e4` found the two
instrumented tests target their intended guards, but identified a P2 handoff
fixture failure path that can strand its barrier worker. Keep that fixture issue
open; the scoped successful runs are not invalidated by it. The operator subsequently approved isolated qualification of a separate
application-status profile under an explicit trusted single-terminal-message
contract; FIN-only behavior remains unchanged and adoption is not authorized.
Parser-only evidence and the P2 fixture cleanup are under separate rereview.

### Current parent verification and recovery enumeration prerequisite

Latest parent finish review remains **BLOCKED:51 canonical test failures plus
unreconciled planning signals**. A separate full owned-database run with the
explicit staged OSV fixture reports five top-level failures across CLI/Gates
(nine failure events including subtests), all at the unconfigured sandbox
boundary. It reports1470 passing test events and four explicit skips: hostile
cluster sandbox, interactive browser fixture, configured live model and real
OpenBao. Log: `/tmp/nf-parent-owned-full-go-current.jsonl`. This does not replace
canonical clearance, and no merge assertion was relaxed.

Parent added `sandbox_recovery.go` and tests plus migration000006's partial index.
RecoveryPage exposes only org/invocation IDs to the exact platform recovery
worker, in bounded target/namespace-pinned keyset pages. ReadForRecovery requires
re-entered organization scope and rejects a different target. Deletion fences
retain unresolved entries; released rows are not enumerated. Reads grant no
create/exec authority. Startup, authenticated RPC and periodic observer wiring
remain absent: this is a store prerequisite, not running recovery.

Four safety mutations (authority, released-row filter, strict cursor and target
readback) failed behaviorally and were restored exactly. Restored scoped
journal/purge/namespaced/recovery races passed41.359s; parent build and Gates vet
returned0. Initial missing-API/type compilation failures are not behavioral red
evidence. Capture `nf-sandbox-recovery-review-fk0y0xmn`; reviewer
`a9d18008-3e5e-4b5c-9e8a-a9312b485f67` returned scoped OK with notes.
Follow-up tests now directly cover release between page reads, insertion behind
the cursor and a restarted sweep, populated create/exec claim readback without
redeeming those claims, and released-record readback without fabricated results.
Offset pagination and erased claim/released-history mutations failed behaviorally;
exact restoration followed by owned-PG race3 passed7.863s. This test-only follow-up
received scoped OK with notes from recovery-boundary reviewer
`fc8ea43c-07bd-4e37-858b-4e263adfed57`; production enumeration and migration
are unchanged. This does not establish concurrent/process-restart or startup/RPC
integration evidence.

The isolated strict status parser received scoped OK from
`febf0181-7dff-4464-8740-cd83ccd1de27`; it is still called only by tests.
The handoff fixture P2 cleanup received scoped OK from
`3909516f-cca5-4ea3-a32e-16d4adeac518`. The status worker's separate slice2
proposal (reset-code evidence, monotonic framing/transport failures, natural
peer-end draining and HTTPS authority binding) received a BLOCK on broad
implementation from reviewer `af9151c1-a5e4-481b-b8e6-f6b794fe89e3` pending
policy decisions and bounded measurement. None is wired or accepted.

### TLS classifier and follow-up measurement preparation

Frozen stdlib experiment `/tmp/nf-tls-classifier.YjuTIiv6` reproduces exact raw
EOF and UnexpectedEOF normalization, and genuine close_notify co-delivered with
bytes+EOF, on installed Go1.27.1. Deferred sticky errors preserve tested bytes
and independent failure evidence. These are source-specific diagnostic results,
not an executor completion rule. Parent checked classifier/test/module hashes
and modes and ran race20 (6.914s): `/tmp/nf-tls-classifier-parent-race20.log`.

Review run `806617a8-82fc-4713-9433-dcefaaca4f87`, workflow
`63f81309-7c20-4aab-a654-52dd7c2ce8a5`, encountered an unavailable child finish
gate and two expired supervisor requests. Parent replies found no pending request;
status showed the child complete. The final artifact was replaced by a provisional
blocked notice. The detailed read-only assessment is preserved from the retained
session in `nf-tls-review-coordination-rg58jjss/assistant-0.md`, together with the
session and current source hashes. It reports one P2 missing positive assertion
for caller publication of a TLS-generated failure, not demonstrated current loss.
The lane cwd is `NovaForge-integration-lanes/gate-isolation`, branch
`integrate/gate-isolation`, HEAD `764c28320f0adf7976340937c9ca1ce8aa40f5ea`.
No alternate execution protocol was used to bypass the coordination failure.

Parent made a fresh copy `nf-tls-publication-u4sb8wcu`, leaving the classifier
unchanged, and added that assertion. Omitting caller publication now fails
behaviorally; exact restoration then passes race20. Logs and baseline/results
hashes are in that capture. No broader TLS or shutdown policy follows.

The operator approved a second bounded parent-owned kw measurement (see
`decisions.md`). Local-only diagnostic preparation is assigned in workflow
`e8bf3d4d-fea4-4272-8284-fe0f1dcca27d`: frame metadata/reset codes/GOAWAY and
termination origin, with bounded time/bytes/events and no status acceptance.
No second live probe has run. Parent retains review, provisioning, UID-scoped
cleanup and all gates. Canonical51 plus planning remains blocked.

The diagnostic artifact is now frozen at `/tmp/nf-bounded-closure.kg94O5tS`.
Parent verified its inventory plus all 39 file hashes/modes and reran local
race20 with the operator test excluded (6.296s), logging to
`/tmp/nf-bounded-closure-parent-race20.log`. This is not a live measurement.
Fresh read-only review in workflow
`0a063e3c-0b02-4319-803f-e925798c2fbe` returned BLOCK: `HandshakeContext`
adds an internal cancellation callback that is not joined, and the live wrapper
can overwrite the scanner termination reason with a later context observation.
Correction is assigned in workflow `ea184fef-1fb4-45b3-95ce-eb78b298a994`, in
a fresh temporary copy with behavioral regressions and subsequent independent
review required before live use. PINGs are recorded but not answered,
and 64KiB bounds consumed SPDY framing bytes, not TLS/HTTP traffic/read-ahead;
both restrict what a later observation can establish.

Parent prepared, but did not execute, an adapted provisioning/cleanup driver
in `nf-live-closure-ciaqy87t`. It uses the same isolated immutable-image profile,
explicit kw request timeouts, and the new standalone diagnostic; final cleanup
requires a subsequent NotFound observation rather than merely a failed GET.
No new cluster resource has been created during this preparation. The driver
now refuses all subprocess/cluster effects without a parent-written review
approval and matching diagnostic source hashes. A local guard test demonstrated
that absent approval fails before any subprocess or namespace identity creation;
the approval file has not been written.

Correction candidate `/tmp/nf-closure-corrections.zv5_l00d` changes only the
handshake and reporting seam among inherited source files, with new direct-TLS
ownership and late-context reporting tests. Parent verified its inventory plus
77 file hashes and reran local race20 with the operator test excluded (6.839s):
`/tmp/nf-closure-corrections-parent-race20.log`. Independent re-review is running
in workflow `95e001a8-2361-4764-a1da-e0321596c975`. The cancellation guarantee
is post-acquisition direct TLS only; DialContext internals remain outside it.
Worker scratch Procoder invocations are not canonical or finish-gate evidence.

The parent driver additionally records timed-out subprocess attempts and partial
output, treats unacknowledged deletion as pending cleanup, and reads back
namespace admission labels, the full policy inventory, service-account identity
and quota before measurement. Local approval-guard and subprocess-timeout tests
issued no cluster requests. At that preparation checkpoint no review approval or live execution had occurred.

### Second bounded kw measurement

Correction review `f72d769d-f4bc-4619-9a55-95d8df2d3a32` returned scoped OK
with notes. Parent consumed it, reverified candidate hashes, and applied the
operator's existing diagnostic-only approval; no adoption or activation followed.

The first driver attempt used an incompatible namespace prefix and stopped at
the diagnostic identity guard before kubeconfig loading or exec. Capture
`nf-live-closure-ciaqy87t` retains that failure and the pod/namespace UID deletion
acknowledgments plus subsequent NotFound. Parent corrected only the driver prefix
in a fresh capture `nf-live-closure-retry-8c_nc6he`, retaining diagnostic bytes.

The successful diagnostic used namespace `nf-transport-probe-7c4c526357` and
requested exit0/exit7. Each recorded ten frames: three SYN replies, stdout/stderr
DATA and FIN, status DATA then numeric reset5, and GOAWAY status0/last-good-ID5.
The next header read returned exact EOF with zero partial bytes. Both recorded
TLS1.3, RawEOF=false, no upper/ledger/context failure, stopped-or-joined owned
callback, and no raw-cleanup error. Wire consumption was206/477 bytes. No PING
or reset-after-output-FIN was observed; absence in these two traces is not a
protocol guarantee. Requested exit is not decoded remote status.

The probe verified TLS to `192.168.10.100:6443` with effective verification name
`192.168.10.100`. Its Go1.27.1-specific classifier distinguishes the tested raw
EOF normalizations; this is not a public TLS shutdown certificate or evidence
of complete executor output. No payloads were logged or accepted. GOAWAY and
closure policies remain operator decisions, not inferred authorization.

Pod UID `cfb36197-8f38-481f-bcd9-e1c617cee704` and namespace UID
`da7c4fec-27c9-4f93-ba3b-2bcfd68a7820` received UID-preconditioned deletion
acknowledgments and subsequent NotFound. This is temporary-resource cleanup
evidence, not physical termination, journal recovery or production isolation.
The capture includes 49 hashed files, metadata observations and full command
receipts. No existing Helm resource was changed. Read-only evidence/profile
assessment is running in workflow `fa065c4d-0279-4727-93bf-b6decae6fb08` before
further policy/qualification choices. Canonical51 plus planning remains blocked.

That assessment supports the narrow measurement interpretation but blocks profile
adoption/acceptance wiring. The operator then directed use of recommended choices
by default, authorizing the recorded conservative local-only candidate without
repeated confirmation. No wider adoption or activation authority follows.

The first additive wire-evidence checkpoint is `/tmp/nf-framing-evidence.YAPSD8zV`:
three added Go files, with the legacy reader/receipt/owner and strict parser
unchanged. Parent checked the three reported hashes and all 35 original patched
files byte-for-byte, then ran evidence tests under race20 (1.757s), logged at
`/tmp/nf-framing-evidence-parent-race20.log`. Independent review is running in
workflow `a05a26f7-f390-4d39-b287-4a9030ab2864`.

This reader is not called by Connection/Serve and is not a completion predicate.
Constructor-supplied IDs are not actual admission/transmission evidence; parsed
headers and controls carry explicit unqualified flags. Persistent compressed
header suffix completeness, owner drain/creators/copies, input credit, control
writes and TLS integration remain open. No positive lifecycle candidate exists
at this checkpoint.

Wire-evidence reviewer `4b6e1501-5c3c-4e01-9fa9-625083cbba47` returned scoped
OK with notes: no established P0/P1 defect, but constructor header limits lack
actual-wire boundary tests and the duplicate-reply assertion is masked by a
preceding FIN violation. Compressed-header completeness remains explicitly
unqualified; unused compressed input does not establish absent decoded suffix.
Follow-up workflow `82c5c9c2-8f48-423e-b9da-36813d2bc5cc` is limited to those
regressions and bounded compressed-wire attribution fixtures in a fresh copy,
before owner/lifecycle integration. Legacy production files remain frozen.

The wire-evidence slice remains a bounded diagnostic artifact. No lifecycle completion or production adoption authority is asserted. Parent canonical 51-plus-planning remains BLOCKED.

### Owner/drain/creator boundary

Fresh copy `/tmp/nf-framing-evidence.YAPSD8zV` (patched/spdy/). Two new files
added to `patched/spdy/`:

- `evidence_owner.go` — Owner type; CreatorAdmission constructor (1..4 nonzero odd
  client IDs, immutable copy); Drain() copies evidence state then marks drained;
  Reconcile() checks admission gaps; Status() reads state. Lock ordering:
  evidence.mu → owner.mu (evidence first, never nested). No I/O/queuewait/join
  under evidence.mu. No production lifecycle integration.
- `evidence_owner_test.go` — 11 named tests. All pass under race20 (2.497s). Test
  coverage: constructor validation, copy immutability, admission before/after Drain,
  Drain captures/returns StopError, double-drain, Reconcile with/without admission,
  Reconcile before Drain, SetAdmission after Drain no-op, and lock ordering.

35 baseline files byte-identical. All existing evidence tests, K8s compat suites,
and strict parser suite pass. No legacy source/parser/test changed. Procoder check:
0 hygiene findings. Owner owns evidence state only; does not perform I/O, queue
waits, or joins. Reconcile gaps are known-not-errors (admitted-but-not-seen
streams). No producer join or TLS qualification. Independent review of the full
slice is running under workflow `48765377-e2a7-4c81-aea6-b9b0ff57aeb5`.

Canonical51 plus planning remains blocked. No lifecycle completion or production
adoption authority is asserted.

Wire-evidence-full-review `a6c96319` returned OK with notes: no P0/P1 defects. Four P2 findings: (1) `TestEvidenceReplyMatrix` conflates duplicate SYN_REPLY with post-terminal frame (no fix needed), (2) constructor 1..4 limit has no test exercising boundary (4 accepted, 5 rejected), (3) `evidence.go:228` guard `r.payload.Len() != 0` is unreachable but serves as documentation, and (4) `Reconcile` silently ignores orphaned admission entries without surfacing them in `Status()`. Full wire-evidence slice: 7 files, all tests pass under race20 (2.993s) and full race1 (1.490s). TLS integration evidence now complete: classifier.go (129 lines), tls_integration_test.go (608 lines), 10 named tests covering TLS 1.2/1.3 handshakes, verified chains, close_notify, deferred classifier, version 772 uint16, ServerName mismatch, baseline mode, zero read — all pass under race20 (34.035s). No lifecycle completion, producer join, TLS integration, or production adoption authority asserted.
