# NovaForge gap closure

Baseline: 2026-09-27. Implementation authorized by the user: “ok lets do it all”. Work is in progress; checkboxes require the stated evidence.

## Goal and completion rule

Close the remaining Git-host functionality gaps, prove the unverified deployment
paths, and reconcile broader platform limitations against the approved spec.
Backend, REST API, CLI and GUI changes travel together wherever applicable.
An item is complete only when its production caller is wired, its relevant
positive and negative paths are tested, and its deployed behavior is evidenced.
A closed historical task is not proof that its entire feature is complete.

The user authorized NovaForge implementation, verification and release work.
Shared infrastructure changes still follow the concrete review boundary in G11.
Existing product exclusions remain in force unless explicitly changed.

## Evidence and baseline

- Claude session `5a8c3920-c543-4e9f-ad19-7fc324dd8240`, September 11–26.
- `BUILD-STATUS.md`, especially the September 26 checkpoint and Known limitations.
- `.procoder/specs/backend-platform.md` and `traceability.yaml`.
- `.procoder/plans/git-parity.md` and the nine corresponding closed task records.
- Local inspection: `main` at `f314f75`, initially clean, 38 commits ahead of the
  locally recorded `origin/main`. No remote refresh was performed for this plan.
- Last recorded deployment: `dc5f70f`, Helm revision 9; 13/13 cluster suites pass;
  43/47 acceptance criteria covered, four partial. This is historical evidence,
  not a fresh test run or live-health check.

Read-through confirmed the principal implementation gaps: webhook delivery reads
only the push stream; SSH accepts only Git upload/receive commands; `RunHead`
retains the source SHA but not the source repository, and workspace construction
reads that SHA from the target repository. The webhook and import network limits
are explicit in both code and chart configuration.

## Sequence and milestones

| Milestone                        | Work    | Exit condition                                                                               |
| -------------------------------- | ------- | -------------------------------------------------------------------------------------------- |
| M0: trustworthy baseline         | G01     | Requirements, limitations and current evidence agree                                         |
| M1: complete core Git flows      | G02–G05 | Fork gates, webhook events, SSH LFS and object cleanup work through deployed paths           |
| M2: migration and user workflows | G06–G07 | Controlled external connectivity and complete CLI/GUI workflows are proven                   |
| M3: operational proof            | G08–G09 | Certificate renewal and an approved deployment execute on kw                                 |
| M4: broader platform disposition | G10–G11 | Every remaining limitation has evidence, a concrete follow-up, or an explicit scope decision |
| M5: release acceptance           | G12     | Final immutable deployment and all old/new acceptance suites pass                            |

Recommended execution order: G01 → G02 → G03 → G04 → G05 → G06 → G07 → G08
→ G09 → G10 → G11 → G12. G07 acceptance cases should be added alongside their
features; G07 is the combined user-workflow checkpoint. No calendar estimate is
assigned before G01 and the deeper source/target gate audit in G02.

## G01 — Reconcile requirements and establish the live baseline

Dependencies: none. Areas: spec, traceability, existing plans, BUILD-STATUS.

- [x] Correct superseded scope prose: GUI is implemented; S-27 adds import and
      mirroring despite the old exclusion; specify SSH-authenticated LFS scope.
      Preserve wiki, portfolio management and other actual exclusions.
- [x] Separate historical failures from current limitations. The September 25
      agent timeout is not a current failing-suite verdict after the September
      26 full pass. Preserve the history and identify what remains unverified.
- [x] Reconcile old unchecked plan steps with newer task and deployment evidence;
      do not mechanically reopen or close them. Track new work through the
      existing task workflow when implementation starts.
- [x] Read kw deployment image IDs, Helm state, service readiness and datastore
      availability. Refresh remote refs before deciding what needs pushing.
- [x] Establish a reproducible test baseline with `hack/env.sh` sourced and real
      datastores; record failures as code, environment, or external dependency.

Acceptance: a dated baseline with exact commit/image identities, tests actually
executed, explicit skips, and a gap-to-requirement mapping. No coverage upgrades
from document editing alone.

## G02 — Evaluate cross-fork changes against the parent's policy

Dependencies: G01. Requirements: S-10, S-11, S-30.
Areas: `internal/gates/{controller,wiring,approvals}.go`, gate persistence and
sandbox evidence, `internal/reviews/`, Git RPCs, `web/src/screens/RunDetail.tsx`.

- [x] Carry immutable source repository identity alongside source SHA and target
      repository/policy SHA through lookup, materialization and evaluation.
- [x] Read changed files from the fork; read gate definitions and approval policy
      from the target. Audit diff-based approvals and API-compatibility baseline
      reads too: fixing workspace checkout alone is insufficient.
- [x] Bind cached evaluations and evidence to the relevant source and target
      identities/revisions; fail closed on deletion, lost access or changed heads.
- [x] Show the source repository/ref and useful gate errors through API and GUI.
- [x] Add a real cross-fork cluster acceptance flow with an executable tests gate.

Acceptance: a passing fork change satisfies the parent's gate and merges after
independent approval; a failing change is refused for that gate; weakening gate
files in the fork has no effect; source/target changes invalidate stale authority;
cross-organization requests remain refused. No parent-history mutation is needed
merely to evaluate the fork.

Cross-organization forks are a separate product/security decision, not part of
this correction to same-organization forks.

## G03 — Publish Engineering Run and CI webhook events

Dependencies: G01. Requirement: S-24.
Areas: `internal/events/`, `internal/reviews/`, `internal/ci/`,
`internal/webhooks/`, service entrypoints, hook API and Repos screen.

- [x] Define versioned event payloads with stable event ID, organization,
      repository, object identity, transition and timestamp. Specify Engineering
      Run transitions and aggregate CI terminal outcomes, including cancellation.
- [x] Persist publication intent atomically with the owning service's state
      transition and relay it to Redis; follow the existing deployment outbox
      pattern where appropriate. No direct cross-schema queries.
- [x] Extend worker routing, subscriptions, signed payloads and delivery history.
      Persist retry accounting across restarts and deduplicate event scheduling.
- [x] Expose event selection and delivery outcome in the GUI and API contract.

Acceptance: actual run transitions and completed CI runs produce signed,
repository-scoped notifications; Redis outage and worker restart do not lose
committed events; retries terminate and do not leak across organizations. Document
at-least-once delivery and stable IDs rather than promise exactly-once HTTP.

## G04 — Make SSH-cloned repositories work with Git LFS

Dependencies: G01. Requirement: S-25.
Areas: `internal/gitops/{ssh,lfs,http}.go`, transport authentication,
`cmd/git-platform/`, configuration/chart, Repos clone instructions.

- [x] Add the narrowly parsed `git-lfs-authenticate` SSH command and return the
      existing HTTPS LFS endpoint plus an expiring, repository/operation-scoped
      credential. This is SSH authentication with HTTPS object transfer.
- [x] Apply the same actor, collaborator, archive and capability restrictions as
      the existing transports; never exchange an SSH key for a broad PAT.
- [x] Configure an externally usable TLS endpoint and explain the transport in
      user-facing clone instructions without exposing credentials.

Acceptance: an unmodified git-lfs client clones and pushes large random content
from an SSH remote with a valid certificate chain; expired, wrong-repository and
wrong-operation credentials fail; an archived repository refuses upload. Ordinary
Git SSH behavior remains covered.

## G05 — Reclaim deleted LFS and release payloads

Dependencies: G01. Requirements: repository/organization deletion, S-25, S-28.
Areas: `internal/gitops/{purge,grpc,lfs,releases}.go`, migrations,
`internal/blobstore/`, existing deletion consumers.

- [x] Persist scoped cleanup work before cascading metadata deletion; implement
      bounded, retryable cleanup through the production worker entrypoint.
- [x] Cover repository and organization deletion, individual release deletion,
      replaced assets and interrupted uploads. Fence concurrent uploads so they
      cannot recreate garbage after deletion has been marked complete.
- [x] Account for repository transfer and fork object ownership before selecting
      keys. Use durable IDs, not a repository name that can be reused.
- [x] Plan a bounded reconciliation pass for already orphaned objects, with a
      grace period and report-only mode before deleting historical candidates.

Acceptance: real MinIO objects disappear after committed deletion; unrelated
repositories with identical content survive; storage outages/restarts retry
without losing cleanup intent; same-name recreation is safe. Report pending and
failed cleanup explicitly.

## G06 — Support controlled external imports, mirrors and webhooks

Dependencies: G01, G03. Requirements: S-24, S-27, air-gapped deployment model.
Areas: mirror client, webhook HTTP client, chart network policy and values,
configuration, Repos/settings UI.

Proposed posture: retain air-gapped defaults and add an explicitly configured
connected mode with operator-approved destinations. Do not simply remove
git-platform from `networkPolicy.airGapped` and grant unrestricted egress.

- [x] Define application destination rules and enforceable cluster egress rules
      together, including DNS, redirects, IPv4/IPv6, internal endpoints and proxy
      behavior. Choose the enforcement mechanism after checking kw capabilities.
- [x] Allow deliberately approved internal hosts while preventing arbitrary
      access to metadata/control-plane endpoints. Preserve credential secrecy.
- [x] Distinguish policy refusal from remote authentication and availability
      errors in REST/GUI responses and mirror status.
- [x] Prepare rendered manifests and rollback instructions for the selected
      destinations before applying any network-policy expansion.

Acceptance: import from an approved external Git host, refresh after an upstream
commit, and deliver to an approved receiver; unapproved destinations remain
blocked; default air-gap tests still pass. Use a controlled external test fixture
before touching an actual migration repository.

## G07 — Complete CLI and GUI workflows and migration evidence

Dependencies: G02–G06 for the full checkpoint. Requirements: S-21, S-24–S-30.
Areas: `internal/cli/`, edge/OpenAPI, `web/src/lib/api.ts`, Repos/RunDetail screens,
`tests/e2e/`, `hack/e2e-in-cluster.sh`.

- [x] Inventory existing commands before adding fork, import, mirror status,
      refresh and stop-mirroring commands. Cover cross-fork run creation too.
- [x] Supply credentials without command-line arguments or printed secrets.
- [x] Verify GUI creation, status, failure and recovery paths using actual users
      and repositories, not pre-seeded display data.
- [x] Add cluster suites for webhook events, SSH LFS, forks, import/mirroring and
      blob cleanup; register them in the acceptance harness.
- [x] Document migration fidelity: refs/history are distinct from LFS payloads,
      release assets, users, issues and CI metadata. Test what import actually
      copies and make any unsupported data explicit before calling it migration.

Acceptance: `nf` and the GUI can complete the supported workflows end to end;
imported refs and content match the source; mirror-to-writable conversion behaves
as documented; no unsupported Gitea parity is implied by Git-history import.

## G08 — Prove certificate rotation without restarting Git service

Dependencies: G01. Requirement: S-29.
Areas: certificate Helm template, TLS reload implementation, deploy acceptance.

- [x] Add a dedicated test certificate/service fixture using the deployed reload
      path; induce cert-manager renewal without modifying a shared issuer.
- [x] Observe the changed certificate serial and a fresh TLS connection serving
      it, with hostname/CA verification enabled and unchanged pod restart count.
- [x] Clone and push before and after renewal; preserve HTTP refusal on TLS port.

Acceptance: evidence ties cert-manager renewal, mounted Secret update and live
server reload together. An in-process keypair replacement alone does not close it.

## G09 — Activate and prove governed application deployment

Dependencies: G01. Requirements: S-11, S-12, S-22.
Areas: `internal/deployment/`, `internal/secrets/`, `cmd/deployment-runner/`,
`deploy/docker/Dockerfile.deployment-runner`, deployment configuration and GUI.

- [ ] Supply a small controlled chart and build the operator-owned runner image
      through kw BuildKit, pinning chart checksum and image digest.
- [ ] Configure a disposable target, narrowly scoped credentials and approval
      rules through the existing configuration path.
- [ ] Exercise requested → approved → executing → observed result through API
      and GUI; prove denial, expiry, failure and retry behavior as well.
- [ ] Verify cleanup and credential revocation, plus deployment evidence reaching
      the graph through the production event path.

Acceptance: an approved action creates the expected workload at the permitted
target; an unapproved or changed action cannot deploy; retries do not duplicate
execution; failures are visible. A controlled fixture proves the product path;
real staging/production target onboarding remains operator configuration.

## G10 — Audit broader agent and intelligence requirements

Dependencies: G01; reconcile G09 results. Requirements: S-7, S-13–S-18, S-20.

The following are candidate gaps, not automatically new implementation scope.
Inspect existing production callers and tests before writing follow-up tasks:

| Area                       | Required disposition/evidence                                                                                                                               |
| -------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Index/graph breadth        | Map Tree-sitter/LSP/SCIP paths, languages, graph edge kinds and branch scope to S-14/S-15; identify unsupported requirements explicitly                     |
| Knowledge recall           | Prove a recorded decision enters a later relevant agent run on the real cluster; preserve provenance and disclose retrieval limitations                     |
| External MCP               | Verify production HTTP authentication support and stdio routing; if missing, design credential references and isolated stdio execution before enabling them |
| Agent CI sponsorship       | Define whether delegated sponsorship for agent/service pushes is required; never silently invent a human actor                                              |
| Workspace dependencies     | Document offline vendoring/cache workflow and test it; do not open agent internet egress to make builds pass                                                |
| Cost budgets               | Demonstrate configured pricing and budget enforcement in an isolated fixture; retain explicit refusal when pricing is absent                                |
| Dependency scanning        | Verify manifest coverage against promised ecosystems and define offline advisory refresh before expiry                                                      |
| Direct default-branch push | Reconcile member push rights with production-credential and deployment approval requirements                                                                |

Acceptance: each row gets implementation evidence, a bounded follow-up with
acceptance criteria, or an explicit scope decision. A 43/47 structural traceability
count is not proof that the broader prose requirements are completely fulfilled.

## G11 — Resolve operational follow-ups without expanding project authority

Dependencies: G01. These items have different owners from NovaForge code.

- [ ] Nexus storage: recommend scheduled bounded trim during a maintenance
      window, compare it with mount-time discard, and prepare the exact target,
      privilege scope, schedule and rollback for review. Do not operate broadly
      on every host filesystem. Existing one-off trim is not recurring cleanup.
- [ ] OpenBao: preserve the explicitly accepted development setup; document a
      separate production readiness path for off-cluster unseal custody.
- [ ] Model gateway: verify recurrence before reopening the old incident. Update
      the existing issue in its own project if needed; do not modify its code.
- [ ] Measure registry growth after retention begins; do not repeat the old
      “eight months” estimate (756.8 GiB / roughly 14 GiB per day is about 54
      days before retention effects, not eight months).

Acceptance: operational proposals are concrete and independently reviewable;
accepted development choices remain identified as such, and external issues have
an owner rather than being misreported as NovaForge implementation failures.

## G12 — Verify and publish an honest release checkpoint

Dependencies: core milestones complete; broader follow-ups explicitly accounted for.

- [ ] Run relevant red/green regressions during implementation using real Git,
      PostgreSQL, Redis and MinIO; use isolated test databases for destructive
      fixtures. Add negative authorization and failure/restart cases where needed.
- [ ] Run the full Go suite with datastore prerequisites loaded, build/vet/format,
      frontend types/build, generated-contract checks and repository commit gates.
- [ ] Build immutable images and deploy through `hack/deploy.sh`; never bypass
      Helm ownership or the image/credential preflight.
- [ ] Run the original 13 acceptance suites plus all new registered suites on the
      exact deployed revision; browser-test the changed user flows.
- [ ] Update traceability, task evidence and BUILD-STATUS with commit, image IDs,
      suite results and remaining limitations; close tasks through their workflow.
- [ ] Verify the remote state and publish the verified commits under the release
      scope agreed for implementation. Planning alone does not push anything.

Release criterion: the four partial criteria become covered only when their own
missing behaviors have evidence. Any broader deferred decision stays visible;
do not label this full Gitea equivalence or production readiness from suite counts.
