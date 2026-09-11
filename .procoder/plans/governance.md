# governance — implementation plan

Status: complete
Spec: .procoder/specs/backend-platform.md

## Goal

Make the platform, not the agent, the authority on merge: a gate controller that runs after an
agent declares completion and cannot be bypassed or self-approved, a policy-driven approval
model the model can never alter, and a secret broker issuing short-lived scoped credentials
instead of durable secrets.

## Architecture

One new Go service, `gates`, owning its own PostgreSQL schema and gRPC API. It is the sole
writer of merge eligibility: the reviews service asks it whether a run may merge and never
decides for itself. Gate definitions are read from the repository's .novaforge directory at the
target ref, never at the source ref, so a change cannot weaken the gates that judge it.

## Constraints

Taken verbatim from the spec; every task inherits these.

- Go 1.26 or later, PostgreSQL 16 or later with pgvector, Redis 7 or later with Streams and
  consumer groups, Kubernetes 1.29 or later.
- Internal transport is gRPC; the client edge is REST described by OpenAPI; events are Redis
  Streams.
- One PostgreSQL cluster, one schema per service. No service reads another service's tables;
  cross-service reads go through that service's gRPC API.
- Organizations are a hard security boundary: per-org namespaces, per-org credentials, and no
  data path between orgs. Every authorization check is org-scoped, and no query may be
  satisfiable without an org predicate.
- Agents cannot bypass gates — enforcement is structural, not convention.
- A gate controller that cannot be reached means merge is blocked, never permitted. Every gate
  path fails closed.
- The model never decides its own permissions; approval requirements come from policy.
- The secret broker fails closed: jobs requiring credentials block while jobs requiring none
  continue to run.
- ProCoder is invoked as a binary by the gate controller inside the job image; its exit status
  and structured output are parsed into gate results.
- Air-gapped operation is a first-class deployment model. No feature may hard-depend on
  reaching a hosted model provider or the public internet.
- Redis durability is AOF persistence with consumer-group redelivery, giving at-least-once
  delivery. Every stream handler must be idempotent.
- Kubernetes and Helm are the only supported deployment path; docker-compose is out of scope.
- Module path is `github.com/novaforge/novaforge`. Service binaries live under `cmd/<service>`,
  shared packages under `internal/<domain>`.

## Task 1: Gate definitions and evaluation schema

Files: `internal/gates/migrations/000001_gates.up.sql`,
`internal/gates/migrations/000001_gates.down.sql`, `internal/gates/store.go`,
`internal/gates/store_test.go`

Interfaces: produces
`gates.Definition{Name string, Params map[string]any, Required bool}`,
`gates.Evaluation{ID, OrgID, RunID uuid.UUID, Gate string, Status string, Detail string, EvaluatedAt time.Time, TargetSHA string}`,
and `gates.Store` with `RecordEvaluation(ctx, Evaluation) error`,
`ListEvaluations(ctx, runID uuid.UUID) ([]Evaluation, error)`, and
`LatestForSHA(ctx, runID uuid.UUID, sha string) (map[string]Evaluation, error)`.

- [ ] Write the up migration creating `gate_evaluations` (id uuid pk, org_id uuid not null,
      run_id uuid not null, gate text not null, status text not null check (status in
      ('pass','fail','error','skipped')), detail text not null default '', target_sha text not
      null, evaluated_at timestamptz not null default now(), unique (run_id, gate, target_sha))
      plus `CREATE INDEX ON gate_evaluations (org_id, run_id);` and the matching down migration.
- [ ] Write the failing test `internal/gates/store_test.go`:
      `func TestRecordEvaluationUpserts(t *testing.T)` records `tests` as `fail` then as `pass`
      for the same run and SHA, and asserts exactly one row remains with status `pass` — proving
      an at-least-once redelivery cannot create duplicates;
      `func TestEvaluationsAreShaScoped(t *testing.T)` records a pass for SHA `aaa`, then asserts
      `LatestForSHA(run, "bbb")` returns no entry for that gate, so a new push invalidates prior
      results;
      `func TestListIsOrgScoped(t *testing.T)` asserts a list under org A never returns org B's
      evaluations. Run `go test ./internal/gates/` — expect FAIL with "undefined: gates.Store".
- [ ] Implement `RecordEvaluation` as
      `INSERT ... ON CONFLICT (run_id, gate, target_sha) DO UPDATE SET status=EXCLUDED.status, detail=EXCLUDED.detail, evaluated_at=now()`.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/gates/` — expect PASS.
- [ ] Commit as `feat: add gate evaluation schema scoped to run and commit`.

## Task 2: Gate definition resolution from the target ref

Files: `internal/gates/resolve.go`, `internal/gates/resolve_test.go`

Interfaces: produces
`gates.Resolve(ctx context.Context, git gitv1.GitServiceClient, orgID, repoID uuid.UUID, targetRef string, workItemGates []string) ([]Definition, error)`.
It reads gate definitions from .novaforge/gates/ at `targetRef` — the branch being merged into —
and unions them with the Work Item's required gates.

- [ ] Write the failing test `internal/gates/resolve_test.go`:
      `func TestResolveReadsFromTargetRefNotSource(t *testing.T)` stubs the git client so
      `main` declares gates `tests` and `security` while the source branch declares none, then
      asserts `Resolve` with `targetRef` `main` returns both — proving a change cannot delete the
      gates judging it;
      `func TestWorkItemGatesAreUnioned(t *testing.T)` asserts a Work Item requiring
      `api-compatibility` adds that gate even when .novaforge/gates/ omits it;
      `func TestUnknownGateNameRejected(t *testing.T)` asserts a definition named `nonsense`
      returns an error containing "unknown gate";
      `func TestMalformedGateConfigFailsClosed(t *testing.T)` asserts invalid YAML returns an
      error and no definitions, so enforcement is never silently skipped. Run
      `go test ./internal/gates/` — expect FAIL with "undefined: gates.Resolve".
- [ ] Implement `Resolve` fetching only at `targetRef`, validating each name against the fixed
      set tests, architecture, security, api-compatibility, dependencies, quality, and
      documentation, and returning
      `fmt.Errorf("unknown gate %q", name)` for anything else.
- [ ] Run `go test ./internal/gates/` — expect PASS.
- [ ] Commit as `feat: resolve gate definitions from the target ref`.

## Task 3: The seven gate runners

Files: `internal/gates/runner.go`, `internal/gates/tests.go`, `internal/gates/architecture.go`,
`internal/gates/security.go`, `internal/gates/apicompat.go`, `internal/gates/deps.go`,
`internal/gates/quality.go`, `internal/gates/docs.go`, `internal/gates/runner_test.go`,
`internal/gates/architecture_test.go`

Interfaces: produces
`type GateRunner func(ctx context.Context, in Input) (Evaluation, error)` where `Input` is
`{OrgID, RepoID, RunID uuid.UUID, WorkDir string, TargetSHA, SourceSHA string, Params map[string]any, Proc ProcoderRunner}`,
plus `gates.Runners` mapping each of the seven gate names to its runner, and
`type ProcoderRunner func(ctx context.Context, workdir string, args ...string) (stdout []byte, exitCode int, err error)`.

- [ ] Write the failing test `internal/gates/runner_test.go`:
      `func TestRunnersCoverEverySevenGates(t *testing.T)` asserts the keys of `gates.Runners`
      are exactly the seven names from Task 2, sorted;
      `func TestTestsGateFailsBelowCoverage(t *testing.T)` stubs `ProcoderRunner` to report 61%
      coverage against a `minimum_coverage` param of 80 and asserts status `fail` with detail
      containing "61";
      `func TestSecurityGateFailsOnSecretFinding(t *testing.T)` stubs a non-zero exit with a
      secrets finding and asserts status `fail`;
      `func TestProcoderErrorIsGateError(t *testing.T)` stubs `ProcoderRunner` returning a
      transport error and asserts status `error` — never `pass`. Run `go test ./internal/gates/`
      — expect FAIL with "undefined: gates.Runners".
- [ ] Write the failing test `internal/gates/architecture_test.go`:
      `func TestForbiddenDependencyDetected(t *testing.T)` builds a workdir where package
      `frontend` imports package `database`, sets the param
      `forbidden_dependencies: ["frontend -> database"]`, and asserts status `fail` with detail
      naming both packages;
      `func TestAllowedDependencyPasses(t *testing.T)` asserts the reverse direction passes.
- [ ] Implement the tests gate by invoking `Proc` with `test` and parsing the coverage
      percentage, comparing against the `minimum_coverage` param defaulting to 0.
- [ ] Implement the security gate by invoking `Proc` with `security` and failing on any secrets
      finding, independent of the SAST result, since the spec marks secrets blocking.
- [ ] Implement the architecture gate by walking the workdir's import graph with
      `golang.org/x/tools/go/packages` and matching against the `forbidden_dependencies` param
      parsed as `"<from> -> <to>"` pairs.
- [ ] Implement the api-compatibility gate by diffing api/openapi.yaml between `TargetSHA` and
      `SourceSHA` and failing when an existing path or required response is removed.
- [ ] Implement the dependencies, quality, and documentation gates by invoking `Proc` with
      `deps`, `lint`, and `docs` respectively, mapping a non-zero exit to `fail` and a transport
      error to `error`.
- [ ] Run `go test ./internal/gates/` — expect PASS.
- [ ] Commit as `feat: add the seven gate runners`.

## Task 4: The gate controller and merge authority

Files: `internal/gates/controller.go`, `internal/gates/controller_test.go`

Interfaces: produces
`gates.Controller.Evaluate(ctx context.Context, runID uuid.UUID) ([]Evaluation, error)` and
`gates.Controller.MayMerge(ctx context.Context, runID uuid.UUID) (allowed bool, reasons []string, err error)`.
The reviews service calls `MayMerge` and merges only on a literal `true`; it has no other path
to merge.

- [ ] Write the failing test `internal/gates/controller_test.go`:
      `func TestMayMergeFalseWhenGateFails(t *testing.T)` records `tests` as `fail` and asserts
      `allowed == false` with a reason naming `tests`;
      `func TestMayMergeFalseWhenGateMissing(t *testing.T)` records nothing and asserts
      `allowed == false` with a reason containing "not evaluated" — an unrun gate is never a
      pass;
      `func TestMayMergeFalseWhenEvaluationIsStale(t *testing.T)` records a pass for SHA `aaa`
      then advances the run's head to `bbb` and asserts `allowed == false`;
      `func TestMayMergeTrueWhenAllPass(t *testing.T)` asserts the only true case;
      `func TestGateConfigEditedInSourceIgnored(t *testing.T)` sets the source branch to declare
      zero gates while the target declares `tests`, records no evaluation, and asserts
      `allowed == false`. Run `go test ./internal/gates/` — expect FAIL with "undefined:
      gates.Controller".
- [ ] Implement `MayMerge` as a deny-by-default fold: start from the resolved definitions, and
      for each required gate demand an evaluation for the run's current head SHA with status
      exactly `pass`. Anything else — missing, stale, `fail`, `error`, or `skipped` — appends a
      reason and leaves `allowed` false.
- [ ] Implement `Evaluate` to run only gates whose latest evaluation does not already match the
      current head SHA, so re-evaluation is idempotent under redelivery.
- [ ] Run `go test ./internal/gates/` — expect PASS.
- [ ] Commit as `feat: add deny-by-default gate controller`.

## Task 5: Reviews service merges only through the controller

Files: `internal/reviews/merge.go`, `internal/reviews/merge_test.go`

Interfaces: produces
`reviews.Merger.Merge(ctx context.Context, runID uuid.UUID, method string) (mergeSHA string, err error)`
where `method` is one of `merge`, `squash`, or `rebase`. It calls
`gatesv1.GatesService.MayMerge` first and returns
`reviews.ErrMergeBlocked` wrapping the reasons when not allowed.

- [ ] Write the failing test `internal/reviews/merge_test.go`:
      `func TestMergeBlockedByFailingGate(t *testing.T)` stubs `MayMerge` returning false and
      asserts `Merge` returns an error satisfying `errors.Is(err, reviews.ErrMergeBlocked)` and
      that the git service's merge RPC was never called;
      `func TestMergeBlockedWhenControllerUnreachable(t *testing.T)` stubs `MayMerge` returning
      a gRPC `codes.Unavailable` and asserts `Merge` returns an error and does not merge —
      proving the path fails closed rather than open;
      `func TestMergeProceedsWhenAllowed(t *testing.T)` asserts the merge SHA is returned and the
      run state becomes `merged`;
      `func TestMergeRequiresIndependentApproval(t *testing.T)` asserts a run with only the
      author's own approval is refused even when every gate passes. Run
      `go test ./internal/reviews/` — expect FAIL with "undefined: reviews.Merger".
- [ ] Implement `Merge` so the very first statement is the `MayMerge` call and every non-true
      result — including any transport error — returns before any git operation is attempted.
- [ ] Run `go test ./internal/reviews/` — expect PASS.
- [ ] Commit as `feat: route every merge through the gate controller`.

## Task 6: Approval policy

Files: `internal/approvals/policy.go`, `internal/approvals/policy_test.go`,
`internal/approvals/migrations/000001_approvals.up.sql`,
`internal/approvals/migrations/000001_approvals.down.sql`

Interfaces: produces `approvals.Action` as a string enum with the values
`read_source`, `modify_workspace`, `add_dependency`, `change_db_schema`, `access_secret`,
`deploy_staging`, and `deploy_production`; plus
`approvals.Decide(ctx context.Context, p Policy, a Action, g capability.Grant) (Decision, error)`
where `Decision` is one of `automatic`, `policy`, `human`, or `forbidden`, and
`approvals.Store` with `Request`, `Resolve`, and `Pending`.

- [ ] Write the failing test `internal/approvals/policy_test.go`:
      `func TestReadSourceIsAutomatic(t *testing.T)` asserts `read_source` and
      `modify_workspace` both decide `automatic`;
      `func TestChangeDBSchemaRequiresArchitectureApproval(t *testing.T)` asserts
      `change_db_schema` decides `human`;
      `func TestDeployProductionForbiddenWithoutGrant(t *testing.T)` asserts a grant with
      `DeployProd == false` decides `forbidden` for `deploy_production`, and `human` when the
      grant allows it;
      `func TestUnknownActionIsForbidden(t *testing.T)` asserts an action outside the enum
      decides `forbidden` rather than defaulting open;
      `func TestPolicyIsNotDerivedFromModelOutput(t *testing.T)` asserts `Decide` takes no
      argument sourced from model output — the signature carries only a policy and a grant.
      Run `go test ./internal/approvals/` — expect FAIL with "undefined: approvals.Decide".
- [ ] Write the up migration creating `approval_requests` (id uuid pk, org_id uuid not null,
      run_id uuid not null, action text not null, detail jsonb not null, decision text not null
      default 'pending', decided_by uuid, decided_at timestamptz, created_at timestamptz not
      null default now()) and the matching down migration.
- [ ] Implement `Decide` as an exhaustive switch with a `default` returning `forbidden`, so a
      newly added action is denied until somebody writes its rule.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/approvals/` — expect PASS.
- [ ] Commit as `feat: add approval policy with deny-by-default decisions`.

## Task 7: Secret broker

Files: `internal/secrets/broker.go`, `internal/secrets/broker_test.go`,
`internal/secrets/migrations/000001_secrets.up.sql`,
`internal/secrets/migrations/000001_secrets.down.sql`

Interfaces: produces
`secrets.Broker.Issue(ctx context.Context, runID uuid.UUID, g capability.Grant, name string, ttl time.Duration) (Lease, error)`,
`secrets.Broker.Redeem(ctx context.Context, token string) (value string, err error)`, and
`secrets.Broker.Revoke(ctx context.Context, leaseID uuid.UUID) error`, where `Lease` is
`{ID uuid.UUID, Token string, Name string, ExpiresAt time.Time}`.

- [ ] Write the up migration creating `secret_values` (org_id uuid not null, name text not null,
      environment text not null check (environment in ('staging','production')), ciphertext bytea
      not null, primary key (org_id, name, environment)) and `secret_leases` (id uuid pk, org_id
      uuid not null, run_id uuid not null, name text not null, token_hash bytea not null unique,
      expires_at timestamptz not null, revoked_at timestamptz, redeemed_count int not null
      default 0) plus the matching down migration.
- [ ] Write the failing test `internal/secrets/broker_test.go`:
      `func TestLeaseExpires(t *testing.T)` issues with a 10ms TTL, sleeps 30ms, and asserts
      `Redeem` returns an error containing "expired";
      `func TestProductionSecretDeniedToStagingGrant(t *testing.T)` issues against a grant with
      `SecretsProd == false` for a production secret and asserts an error containing "not
      permitted";
      `func TestRedeemedTokenIsSingleUse(t *testing.T)` asserts a second `Redeem` of the same
      token fails;
      `func TestCrossRunRedeemDenied(t *testing.T)` asserts a lease issued to run A cannot be
      redeemed in the context of run B. Run `go test ./internal/secrets/` — expect FAIL with
      "undefined: secrets.Broker".
- [ ] Implement values encrypted at rest with `crypto/aes` in GCM using a key from
      `SECRETS_KEK`, storing nonce and ciphertext together, and store only `sha256(token)` for
      leases.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/secrets/` — expect PASS.
- [ ] Commit as `feat: add secret broker issuing short-lived single-use leases`.

## Task 8: Broker-down posture in job scheduling

Files: `internal/ci/credentials.go`, `internal/ci/credentials_test.go`

Interfaces: produces
`ci.ResolveJobCredentials(ctx context.Context, b secrets.BrokerClient, job Job, g capability.Grant) (map[string]string, error)`
returning `ci.ErrCredentialsUnavailable` when the broker cannot be reached and the job declares
at least one secret, and an empty map with a nil error when the job declares none.

- [ ] Write the failing test `internal/ci/credentials_test.go`:
      `func TestCredentialFreeJobRunsWhenBrokerDown(t *testing.T)` stubs a broker returning
      `codes.Unavailable`, passes a job declaring no secrets, and asserts a nil error and an
      empty map — the job proceeds;
      `func TestCredentialJobBlocksWhenBrokerDown(t *testing.T)` passes a job declaring
      `DEPLOY_KEY` and asserts an error satisfying
      `errors.Is(err, ci.ErrCredentialsUnavailable)`;
      `func TestJobStaysQueuedNotFailed(t *testing.T)` asserts the scheduler leaves such a job in
      `pending` rather than marking it `failure`, so it runs when the broker returns. Run
      `go test ./internal/ci/` — expect FAIL with "undefined: ci.ResolveJobCredentials".
- [ ] Implement the split so the broker is contacted only when `job.Secrets` is non-empty, and
      wire the scheduler to skip claiming a blocked job rather than failing it.
- [ ] Run `go test ./internal/ci/` — expect PASS.
- [ ] Commit as `feat: fail closed on secret broker outage without failing safe jobs`.

## Task 9: gates service, REST routes, and deployment

Files: `proto/gates/v1/gates.proto`, `cmd/gates/main.go`, `internal/gates/grpc.go`,
`internal/gates/grpc_test.go`, `internal/edge/gate_routes.go`, `api/openapi.yaml`,
`Dockerfile.gates`, `deploy/helm/novaforge/templates/gates.yaml`,
`tests/e2e/gate_block_test.sh`

Interfaces: produces `novaforge.gates.v1.GatesService` with `Evaluate`, `MayMerge`,
`ListEvaluations`, `RequestApproval`, `ResolveApproval`, `IssueLease`, and `RedeemLease`.

- [ ] Extend `api/openapi.yaml` with
      `GET /api/v1/orgs/{org}/repos/{repo}/runs/{number}/gates`,
      `POST /api/v1/orgs/{org}/repos/{repo}/runs/{number}/gates/evaluate`,
      `POST /api/v1/orgs/{org}/repos/{repo}/runs/{number}/merge`,
      `GET /api/v1/orgs/{org}/approvals`, and
      `POST /api/v1/orgs/{org}/approvals/{id}`.
- [ ] Write the failing test `internal/gates/grpc_test.go`:
      `func TestMayMergeRequiresOrgScope(t *testing.T)` asserts a call with a scope for another
      org returns `codes.PermissionDenied`;
      `func TestEvaluateIsIdempotent(t *testing.T)` calls `Evaluate` twice for an unchanged head
      and asserts the gate runners executed once. Run `go test ./internal/gates/` — expect FAIL
      with "undefined: gates.NewGRPCServer".
- [ ] Implement `cmd/gates/main.go`: migrate schemas `gates`, `approvals`, and `secrets`, serve
      gRPC on 9096, expose `/healthz`, and drain for 30s on SIGTERM.
- [ ] Write `Dockerfile.gates` on `golang:1.26` builder and `alpine:3.21` runtime with
      `RUN apk add --no-cache git`, and install the procoder binary into the image, since the
      gate controller invokes it.
- [ ] Write the failing test `tests/e2e/gate_block_test.sh`: on the kind cluster, push a
      repository whose .novaforge/gates/ requires the tests gate with `minimum_coverage: 80`,
      open a run whose tests fail, assert `nf run merge` exits non-zero with stderr containing
      "blocked", then scale the gates Deployment to zero replicas, assert `nf run merge` still
      exits non-zero, and finally push a change that also deletes the gate definition on the
      source branch and assert the merge is still refused. Run `bash tests/e2e/gate_block_test.sh`
      — expect FAIL with "Error: no matching deployment novaforge-gates".
- [ ] Write the Deployment template and run `bash tests/e2e/gate_block_test.sh` — expect PASS.
- [ ] Commit as `feat: add gates service with merge authority and approvals`.
