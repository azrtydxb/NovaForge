# NovaForge build status

Autonomous build of the backend described in `.procoder/specs/backend-platform.md`,
executed against the six plans in `.procoder/plans/`.

## Latest verification and completion scope (2026-09-16)

Helm revision **70**, implementation commit **d6e07d1**, passed all twelve
in-cluster suites in one run: airgap, deploy, work_ci, gui, search, graph,
factory, agent, agent_ci, merge, cli and crossorg. All service images were
built before deployment and the normal image preflight was preserved.

The preceding full run failed factory because the model supplied `test` as a
Work Item type. The structured output schema now enumerates the actual legal
types; a regression asserts that schema matches `work.SortedTypes()` and was
seen failing before the change. CI artifact decoding/upload errors now fail
the job instead of logging evidence loss and reporting success, also proven
red/green. Speculative gRPC connect-timeout overrides were removed; the bounded
identity RPC lookup remains and has a deadline/handler-context regression.

The full Go suite passed with `hack/env.sh` sourced and `-count=1`; runner,
swarm and svcauth also passed under `-race`. Frontend `npm run build` passed
(including TypeScript). Procoder security and lint reported zero findings.
The SAST integration test now defaults to the committed ruleset instead of
requiring an environment override; `procoder test` subsequently passed.

**This is not whole-product completion.** All 67 historical tasks are marked
closed, but the implementation still needs reconciliation against the broader
spec and known limitations. `.procoder/plans/completion-audit.md` tracks the
remaining audit and implementation work. Passing these suites must not be
used to claim deployment actions, expiring underlying external credentials,
LSP/SCIP indexing, every graph relationship, or enterprise-scale validation.

## Workspace and artifact gap corrections (2026-09-16)

The namespace reaper now honors `novaforge.io/expires-at`, set by the real
agent-runtime provisioning path from `RunCredentialTTL`. A two-hour-old
namespace with remaining lifetime was deleted by the regression test before
the fix and preserved afterwards. Expired, legacy and malformed metadata
still permit orphan cleanup. Provisioning also has a test asserting the expiry
is actually stored.

CI now rejects an absent artifact set when the job declared artifacts and
rejects an unterminated artifact block, even when its tar bytes were otherwise
valid. Both cases previously reported success and were observed red before
the fix. Capture also preserves path whitespace, refuses partially missing
paths, and no longer hides tar failure behind a successful encoding pipeline.
A final log read failure now fails the job rather than logging a warning and
reporting success. The workspace, runner and agentrun packages passed with
`-race` against the configured dev services.

All twelve cluster suites passed at revision 71 (`d0069b2`). The final-log
failure correction was then deployed at revision 72 (`c4ff7e7`), where
`work_ci` and `agent` passed again. Procoder test passed (39 packages) and the
gate reported no blocking findings. The specific long-duration reaper behavior
is regression-tested; the agent suite does not run for multiple hours.

## Maintenance architecture policy (2026-09-16)

The interrupted build resumed with a complete image set for `b3c5549`, deployed
through the normal preflight at Helm revision 73. The expanded factory suite
passed: default-branch architecture policy produces a proposal that remains
unassigned and awaits approval. Procoder test passed (39 packages).

Adversarial review found an isolation defect: failure to resolve repository
gate configuration aborted the whole maintenance scan. The fix in `b75d8d9`
reports the architecture error while continuing unrelated scanners, and
preserves scanner errors in periodic sweep reports. The real Git/database
regression failed before the fix and passed under `-race` afterwards. The
full Go suite passed with dev datastores; lint and security found no issues.
The complete `b75d8d9` image set was deployed through normal preflight at
Helm revision 74. All twelve in-cluster acceptance suites passed in one run;
Procoder test passed (39 packages) and the gate had no blocking findings.
Malformed-policy isolation itself is proven by the real Git/database regression,
not by the cluster factory fixture, which uses valid policy. CI-history,
coverage, benchmark and graph input gaps remain open; this is not whole-product
completion.

## Maintenance CI history input (2026-09-16)

The maintenance sweeper now has a CI gRPC input for completed shell-job logs
on the default branch. It recognizes explicit Go `go test -json` test pass/fail
events, preserving job/package/test identity and commit SHA; it does not infer
individual outcomes from a job exit status. Read failures are scanner errors,
not a clean history. History inspection has a 30-second deadline and examines
only the latest 100 repository runs; CI's metadata list still lacks pagination.
Other test-output formats are not parsed.

The real Git/PostgreSQL/Redis/MinIO regression ran an actual test twice at the
same code version, passing then failing, and required a maintenance proposal.
It failed before wiring and passed afterwards under `-race`. The expanded
`work_ci` cluster fixture adds the same proof through a real runner pod. It
failed on revision 74 because no flaky-test proposal appeared, then passed on
revision 75 (`bc51378`). All twelve suites passed on revision 75, using a
complete immutable image set and normal Helm preflight. The harness archives
committed HEAD; only the post-commit fixture run constitutes the red evidence.

Procoder test passed (39 packages). An uncached full Go run observed
`TestBrokerDownFailsClosed` reading `running` with a broker-blocked detail
rather than `pending`; the subsequent full run passed unchanged. This
intermittent CI status/retry observation was subsequently reproduced and
corrected by the reservation change below; it was not a maintenance-history
defect. Coverage, benchmark and graph maintenance
inputs remain open.

## CI credential reservation correction (2026-09-16)

`ClaimForDispatch` marked a job and run running before credential resolution,
so each broker retry exposed a false running state. The correction reserves
with the existing runner_id while keeping the job pending and started_at empty;
a conditional running transition follows successful credential resolution.
Disconnect cleanup includes reservations, late responses cannot revive terminal
jobs, and cancellation releases reservations with a bounded cleanup context.

The deterministic real-credential-stack regression failed before the correction
and passed afterwards; removing the terminal-state guard independently made
its disconnect case fail. The unchanged broker-down test passed three repeats.
The CI package passed under `-race` and the full uncached Go suite passed.
Commit `b3d2b3f` was deployed at Helm revision 76, with complete immutable
images and normal preflight. All twelve cluster suites passed in one run.
The held-broker timing cases are proven by the real-service regression, not
by injecting outages into the live cluster. Procoder test passed (39 packages),
lint/security had no findings and the gate had no blockers. Broader completion
work remains open.

## Benchmark evidence and database lifetime corrections (2026-09-16)

Maintenance now ingests `benchmarks.txt` through org-authenticated CI artifact
RPCs, using the approved previous-comparable-successful-default-branch baseline.
`BENCHMARKS.md` defines the producer contract, metadata matching, measurement
units, medians and limits. Missing/incomparable evidence remains unavailable;
findings retain both CI run ids and await human approval. On-demand downloads
also forward incoming credentials on streaming RPCs, not just unary calls.

The real Go benchmark/Git/PostgreSQL/Redis/MinIO regression failed before wiring
and passed afterwards. It checks measured allocations of 32 versus 4096 bytes,
skipping failed runs, feature branches, mismatched environments and an older
comparable baseline. Removing environment matching or stream forwarding made
those assertions fail independently. Non-finite measurements and incomplete
artifact streams are rejected; a NaN regression was observed red before fixing
numeric validation.

The first full suite failed because legacy CI test setup truncated shared CI
tables while maintenance was reading them. Exclusive worker tests now create
and remove their own real databases (the test role needs CREATEDB). A regression
proves shared history survives; replacing isolation with the shared pool made
it fail without repeating the destructive truncation.

Isolated database teardown then exposed a production migration connection leak:
`postgres.WithInstance` reserves a sql.Conn that sql.DB.Close does not release.
Migrations now explicitly own and close that connection and their source on
success and failure. Both real pg_stat_activity regressions were red before
this correction, then green. Six session-created test databases left by the
initial teardown failure were removed by exact name, without forced termination.

The affected packages passed under `-race`, the full uncached Go suite passed,
and Procoder test passed (39 packages). Lint and security have no blockers;
the SQL interpolation advisory was audited against the existing strict schema
name validation. The committed `work_ci` fixture failed on revision 76 with
no performance proposal, then passed after `4f7ee61` deployed at revision 77.
All twelve in-cluster suites passed on revision 77, using the complete immutable
image set and normal Helm preflight. Coverage and graph maintenance inputs and
the broader audit remain open. Evidence: /tmp/novaforge-benchmark-{mutation-0,
mutation-1,mutation-2,race-fixed,tests-confirm,red-e2e,e2e,deploy}.log.

## Environment

Everything runs on the **kw cluster** (k3s 1.34, 8 ARM64 nodes). There is no local
Docker daemon — Docker Desktop's containerd store was found read-only and unusable.

| Concern         | How it is done                                                                                                                     |
| --------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| Image build     | In-cluster BuildKit at `tcp://192.168.10.130:1234` over mTLS, `linux/arm64`                                                        |
| Image push      | nexus `192.168.10.131:5000` (the only connector that accepts writes)                                                               |
| Image pull      | nexus `192.168.10.131` on 443 (the only address the nodes trust)                                                                   |
| Test datastores | PostgreSQL 16 + pgvector, Redis 7, MinIO in namespace `novaforge-dev`, exposed as LoadBalancers so tests run against real services |
| Models          | FastLLM proxy in-cluster, reached only through go-ai-sdk                                                                           |

`hack/env.sh` carries all of it. `hack/build-images.sh` builds and pushes;
`hack/deploy.sh` installs the Helm chart; `tests/e2e/deploy_test.sh` is the
acceptance test.

## Method

Work is split across parallel agents in isolated git worktrees, each restricted to
a disjoint set of packages. Every agent works red-green-commit against the plan's
literal test names. **Every agent's result is re-verified by the main agent after
the merge** — an agent's report is a claim, not evidence.

No mocks for PostgreSQL, Redis, MinIO, or git. Model endpoints and the Kubernetes
API use in-process doubles, which is what they are for.

## Proven on the cluster

`bash tests/e2e/deploy_test.sh` passes against the live 8-node ARM64 k3s cluster:

1. All deployments ready.
2. The edge answers `/healthz`.
3. Register, log in, create an organization and a repository through the REST API
   via the `nf` CLI.
4. An **unmodified git client** clones over HTTPS, commits, and pushes.
5. An SSH key is registered through the REST API, and the same repository is
   cloned and pushed over **SSH**.
6. Both pushed commits read back through the REST API.

32 Go packages pass `go test`, 0 failures, against the real PostgreSQL, Redis
and MinIO — no datastore is mocked anywhere.

`bash tests/e2e/work_ci_test.sh` passes: a Work Item is created and listed, a
push schedules a CI run from the repository's workflow file, a runner in its
own pod executes it, and the job's log and declared artifact come back.

`bash tests/e2e/gui_test.sh` passes: the edge serves the web application —
index, client-side deep links, and built assets — without shadowing the API,
and every one of the sixteen endpoints its screens read answers for a freshly
created account.

`bash tests/e2e/agent_test.sh` passes: an agent is defined through the API, a
Work Item is created for it, an Agent Run is started, the run executes against
the real model in its own Kubernetes namespace, and **the agent commits its
work** — a written README on `agents/NF-1/work`, the one branch its capability
grant allows. The test refuses to accept the run's own "succeeded" state as
evidence: that only means the model stopped asking for tools, so it requires
the branch to exist, which it can only do if a commit passed the capability
check.

`bash tests/e2e/factory_test.sh` passes: an epic is decomposed by the cluster's
**real model** into dependency-ordered subtasks — eight on the final run — only
the dependency-free one is startable, and the dashboard answers. This is the
whole model path exercised for real: gateway credential, model choice,
structured output, validation, and materialisation into the work schema.

`bash tests/e2e/agent_ci_test.sh` passes: a workflow whose only job is
`agent: security` is pushed to an organization with **no runner registered**,
the job starts an Agent Run briefed through a Work Item, the run is verified
against that item's acceptance criteria, and the job reports the run's outcome.

`bash tests/e2e/search_test.sh` passes: four unrelated packages are pushed,
indexed with `bge-m3` embeddings through the gateway, and "tax calculation on
a bill" — sharing no word with the code — ranks `invoicing/invoice.go` first.

`bash tests/e2e/merge_test.sh` passes: an author opens an Engineering Run, is
refused approving it themselves and refused merging it unapproved, a second
member approves, and the merge lands on `main` with the run marked merged.

## Defects found by running it, not by reading it

Each was fixed with a test that pins it:

- The edge resolved a bearer only as a personal access token, so every
  authenticated CLI call failed — `nf` presents its session token that way.
- The edge authenticated a request then called the services anonymously.
- A credential says _who_ the caller is, not _which organization_ they act in.
  Sessions therefore carried no org and every org-scoped service refused. The
  org now travels with the request and identity verifies membership.
- The org lookup queried an unqualified table, failing only at runtime, because
  no test covered the path.
- Capability grants were being demanded of human members. Grants constrain
  _agents_; a member has ordinary write access to their own repositories.
- The runner executed repository-supplied commands directly on the runner host.
  Jobs now run in their own Kubernetes pod.
- A mutable `dev` image tag with `IfNotPresent` served cached older images, so a
  redeploy silently ran stale code. Tags are now commit shas.
- Creating a token with no scopes violated a NOT NULL constraint.
- A push-event test read the first message of a durable shared Redis stream, so
  an event from an earlier run won.
- `ClaimJob` and `Dispatch` were both written, both tested, and never called by
  anything. No run ever reached a runner.
- A runner in one organization could be handed another organization's job — the
  claim carried no org predicate, so it leaked the source and its credentials.
- Artifact collection ran `kubectl exec` against a job's pod after the job
  finished, which can never work: the container has terminated. A job now
  captures its declared artifacts before it exits.
- The platform sent no credential to the model gateway and named models
  (`qwen`, `qwen-embed`) that it does not serve, so every model-backed feature
  was dark in a deployment that looked configured.
- The planner refused every decomposition it was given: it validates each
  subtask's agent role against a known set, never told the model what that set
  was, and was wired up with no roles at all.
- Asked for structured output, the cluster's reasoning model spent its entire
  completion budget on chain-of-thought and returned no answer at all. The
  deployment now names the wire parameter that turns thinking off.
- The model put an agent role in a subtask's work-item **type**, and nothing
  checked types until the insert — so a sound eight-subtask decomposition was
  lost half-way through being written. Types are validated before anything is
  written, and a rejected decomposition is retried once with the violation
  quoted back.
- The swarm scheduler, the maintenance scanners and the auto-merger were each
  implemented, unit-tested, and **called by nothing**. A decomposed epic sat
  with its ready subtasks open forever, which from outside is indistinguishable
  from a platform that decided not to start them.
- Three configuration fields were declared, rendered into the chart, set in the
  pod's environment, and never read, so the service reported a feature
  unconfigured while its operator could see the variable set. A test now fails
  on any field `LoadConfig` forgets.
- Four typed agent tools and four MCP tools reported that no service had an RPC
  behind them. Seven now do; the eighth needed no new RPC, only the two-step
  lookup nobody had written.
- No agent workspace could ever be created: the namespace carried its creation
  time as an RFC 3339 label value, and a label value may not contain a colon.
  No agent pod could start either: it declared a repository volume with an
  empty `persistentVolumeClaim.claimName`. Both passed every unit test, because
  the client-go fake validates neither.
- Every tool was offered to the model under one generic schema with its own
  name as its description, so the model guessed each tool's arguments — and
  sent `git.commit`'s `files` as a string, every time, until the run went over
  budget.
- The workspace tools wrote to a path the service's own container cannot
  create, so every `workspace.write_file` failed and the agent had nothing to
  commit.
- An agent run's opening turn said only "Begin work on run <uuid>": no work
  item, no repository, no branch.
- A capability grant allows a branch PREFIX, and the run recorded that prefix
  as its branch — a ref may not end in a slash, so the agent could not use what
  it was given, and every sensible alternative was outside the grant.
- The sign-in screen posted `totp` and read `token`; the edge takes
  `totp_code` and returns `session_token`, so signing in never worked against
  the real service. The GUI acceptance test found it on its first run.
- `secrets.Broker.Revoke` matched on lease id alone, with no organization
  predicate — one organization could revoke another's live credential and
  stall its runs. The same defect class as the CI job claim.
- Three services kept their own auth interceptor that understood a person's
  credential but not a platform service token, while git-platform understood
  both — so an agent's token was accepted by one service and refused by three.
- A fix for one log defect hid the previous one: the follow that held its
  latest line back (so a kubelet-invented line could not be trusted) left a
  print-then-sleep job's log empty for its whole running life, and the whole
  log arrived in one burst racing the status flip to success. The runner now
  polls the log without following, which also makes the inotify defect
  unrepresentable.

## Closing the pending list (2026-09-14)

Eleven items were open. Each is closed below, or said plainly not to be.

- **Gates and maintenance scanners ran a contract that never existed.** Five
  gates and two scanners parsed JSON from `procoder` subcommands that print no
  JSON, from a binary no image contained. They now run real tools
  (`internal/analysis`: go test/vet/list, gofmt, gitleaks, osv-scanner,
  semgrep with a vendored gosec ruleset) in an analysis image used by gates and
  work-reviews. Verified present in both pods on the cluster.
- **A CI job with an agent role passed with no agent.** Runners ran an empty
  command and reported success. ci-runner now executes agent jobs as Agent
  Runs (spec S-6; `TestAgentCIJob`, `agent_ci_test.sh`).
- **An agent run "succeeded" when the model stopped calling tools.** `work.get`
  never even returned the acceptance criteria. A finished run is now verified:
  the model sees the criteria and only the run's tool calls, every criterion
  must be met, and the verdicts are recorded as `run.verification` and shown
  with the run's tool calls.
- **Agent files were staged where nothing could use them.** The workspace pod
  exited at start. It now stays up, holds the repository at its default branch
  as a git repository, and is the run's workspace over exec (`workspace.run`
  runs commands there with no network; `git.commit` without files commits what
  is staged). Proven by a test that provisions a real pod on the cluster.
- **Semantic search could not have worked** (no indexer credential, first push
  undiffable, no embedder key, 768-wide columns for a 1024-wide model, no
  route). Fixed and proven by `search_test.sh`.
- **GUI actions:** cancel an Agent Run (the loop now actually stops — it used
  to keep committing after a cancel), delete a repository (owner/admin only),
  open an Engineering Run, approve or dismiss a maintenance proposal (a run
  cannot start on an unapproved one), toggle a gate (commits to a
  `gates/<gate>-*` branch and opens a run; main is untouched), and request,
  approve, reject or revoke an external MCP server. Each was driven in a
  browser against the cluster.
- **Test organizations:** 57 removed with `hack/purge-orgs.sh`; every e2e script
  now removes its own organization on exit.
- **The model gateway key** was reissued through FastLLM's admin API (via its
  documented `set-password` bootstrap), the key that had been written straight
  into its database was revoked, and the temporary admin login was deleted.
- **Spec traceability:** `.procoder/specs/traceability.yaml` maps all 33
  criteria to real tests, enforced by `TestSpecTraceability` (see below).

Driving the new screens in a browser found four more defects, each fixed:

- **Every merge was refused.** work-reviews called the gate controller with no
  credential; MayMerge answered "no authorization scope" and the merger
  reported the gates unreachable. It failed closed, which looks exactly like
  policy working, and no suite had ever merged. `merge_test.sh` now does.
- **A review was recorded under whatever reviewer id the caller named.** The
  GUI sends none, so no verdict could be recorded from it; a caller who sent
  one could approve their own run as someone else.
- **No member could be added from the interface.** Identity accepted only
  UUIDs; the GUI sends an organization name and a username. Any member could
  also add members; now only an owner or admin can.
- **The maintenance sweep never ran.** Its first sweep waited a full 24-hour
  interval from process start, and every deploy restarts the process. It now
  sweeps shortly after start, and a person can scan on demand.

## Approvals, gate edits and brokered credentials (2026-09-15)

S-10, S-11 and S-12 were partial or uncovered because the components had no
production caller. Joining them found two defects:

- **Every gate judged the target branch, not the change.** The gates service
  resolved a run's head as its target branch, so the tests gate ran against
  main as it already was and a change with failing tests passed it. Found by
  `TestGateBlocksMerge`, which runs identity, git-platform, reviews and gates as
  real gRPC servers and merges through work-reviews' own gates client.
- **An approval's decider was whoever the request named**, and the approvals
  store resolved a request with no organization predicate.

What production now does:

- The gate controller reads each run's diff against its merge base. A change
  under `.novaforge/gates` (weakening, deleting, or proposed through the gate
  proposal flow) or to the database schema raises an approval request bound to
  the change's head; `MayMerge` refuses until an owner or admin who is not the
  author approves it, and a later push needs a new approval. A change that adds
  a dependency (go.mod, package.json, requirements*.txt, Cargo.toml) follows the
  policy path: nobody is asked and the dependencies gate becomes required.
  Every decision is recorded as the run's proof. The Exceptions screen is the
  approvals inbox; a run's page shows what is blocking its merge.
- A CI job's declared `secrets:` are brokered at dispatch: the pump asks the
  gates broker, as a service, for a single-use lease per secret; the value
  reaches the job through a Kubernetes Secret, and is masked in its log by the
  runner and again by ci-runner. A staging job gets staging values only; a
  production job gets production values only on the default branch. With the
  broker unreachable such a job stays pending, blocked with the reason, and
  credential-free jobs keep running. Owners and admins register secrets from
  the Secrets screen; no read returns a value.

None of this has been built into images or run on the cluster yet;
`work_ci_test.sh` step 8 is written and not run.

## Live CI logs and artifact downloads (2026-09-15)

`TestRunnerJobStreamAndArtifact` now drives the runner protocol over a real
gRPC listener — the runner's own `runner.Session`, the pump, LogChunks into
Redis, `GetJobLogs` — and reads a job's log while the job is provably still
running. Writing it found:

- **A pod job's log was never live.** The pod executor collected the whole
  output and forwarded it when the pod exited, to keep the artifact block out
  of the log. Only that block is now held back.
- **No log was ever sealed.** `LogSink.Seal` had no caller; every job's log
  stayed in Redis and the retention sweep had nothing to delete. A runner's
  terminal status report now seals it, and a read merges sealed and live
  lines, since the last chunks can land after the report.
- **Listing one job's artifacts always failed**: the job id was ignored and the
  request resolved as an empty run id.
- **The CI screen never knew a run was running**: it read `body.run`, which the
  edge never sent, so the log was never refetched.
- **Any runner could write into any job's log or report any job's status** by
  naming its id.

Artifacts download through a streaming `DownloadArtifact` RPC and
`GET /api/v1/orgs/{org}/repos/{repo}/ci/artifacts/{id}`, always as an
attachment and never with a renderable type. The work_ci e2e steps that read a
running job's log and download the artifact are written but have not yet been
run against the cluster.

## The maintenance sweep, end to end (2026-09-15)

The sweep moved from `cmd/work-reviews` into `maintenance.Sweeper`, and
`TestMaintenanceProposesWorkItem` runs it as production builds it: a
repository holding `golang.org/x/text v0.3.0` served by the real git service,
the real osv-scanner, a security proposal with no assignee awaiting approval.
It found that **the sweep skipped every organization without a Work Item** —
it listed organizations through the work schema — so a new organization's
first vulnerability could never be proposed. Organizations now come from
git-platform's `ListOrganizationsWithRepositories`, which answers only a
platform token (`svcauth.MintPlatform`, which names no organization and which
every org-scoped path refuses) and returns ids only; the sweep then mints an
org-scoped token per organization. `factory_test.sh` step 5 pushes a
vulnerable `go.mod`, scans on demand and asserts the proposal; it is written
but not yet run against the cluster.

## Deleting a repository or an organization (2026-09-15)

Deleting a repository used to publish nothing, so its Work Items, CI runs and
artifacts, reviews, index and Agent Runs stayed behind, unreachable; an
organization could be deleted only by `hack/purge-orgs.sh` writing across every
schema. Now:

- git-platform's `DeleteRepo` announces `stream:git:repo-deleted` before
  removing anything, and refuses to delete what it cannot announce.
- identity's owner-only `DeleteOrg` (the name must be typed again) announces
  `stream:identity:org-deleted`, then removes the organization and its
  memberships; accounts are kept. `DELETE /api/v1/orgs/{org}` and a danger
  zone on the Orgs screen call it.
- Every service consumes the announcements and deletes only its own share
  (`internal/cleanup`): work-reviews (Work Items, proposals, comments,
  Engineering Runs), ci-runner (runs, jobs, artifact rows **and objects**,
  sealed and live logs, runners, retention policy), engineering-graph (graph,
  code chunks, knowledge), agent-runtime (cancels, then removes Agent Runs and
  tool calls; agents), gates (evaluations, approvals, leases, secrets), mcp-server
  (registered servers) and git-platform (repositories on disk, grants). Runs a
  service cannot trace to a repository reach gates as `stream:runs:deleted`.
- Messages are acknowledged only once handled, so a failed deletion is retried;
  every purge is idempotent. The consumers are proven against the real
  datastores; the deploy e2e step that deletes a repository and an
  organization on the cluster is written but not yet run.
- `hack/purge-orgs.sh` is now break-glass; the e2e scripts delete their
  organizations through the API (`hack/delete-org.sh`) and fall back to it.

Not covered: a schema added on another branch after this change
(`graph.file_references` exists in the shared dev database) is not purged
until its owner's purge learns it.

## Work Items and Engineering Runs through the API (2026-09-15)

`TestWorkItemLifecycle` and `TestEngineeringRunProof` drive the REST handlers
against the real work, reviews and git services. They found:

- **No Engineering Run was ever opened for an agent's work.** A succeeded Agent
  Run's commits stopped on its branch, with no plan, impact, proof or record of
  agent and model, and `RecordProvenance` had no caller. agent-runtime now
  records provenance and opens a run authored by the agent, naming the model it
  ran on, with the Work Item's acceptance criteria as its plan.
- **Change impact counted changes the run never made.** It diffed the branch
  against the target as it is now, so everything merged to the target after the
  branch was cut counted, in reverse — auto-merge's size cap included. Impact
  and the run's Changes tab now diff from the merge base.
- **Change impact had no route.** `GET .../runs/{number}/impact` returns files,
  lines, paths and a risk level with the rule that set it; RunDetail shows it.
- **A Work Item could require a gate that does not exist** and never be
  mergeable; unknown gates are refused. The Work screen can now set
  constraints and required gates, and a Work Item can be assigned to a person
  or an agent from its page.

## The GUI

`web/` implements "NovaForge GUI.dc.html" from the claude.ai/design project
"NovaForge GUI design": all seventeen screens, behind the design's
hover-expanding rail and project context switcher. React 19 + TypeScript +
Vite, embedded into the edge binary at image build time so the UI and the API
it talks to are always the same commit.

Building it needed nine new REST operations and the services behind them:
per-agent run history, a run's plan, the tool calls of the agent run behind an
Engineering Run, project knowledge, symbol relations, maintenance proposals,
the approval policy, the MCP tool list, and the secrets and leases surface.
Two of those were already answerable by a service and had no way for a person
to reach them; the rest needed a store query or an RPC.

## Driven in a browser

The GUI was exercised with Chrome DevTools against the live cluster, doing the
whole loop through the interface rather than through the API: create an
account, create an organization, create a repository, create a Work Item with
acceptance criteria, define an agent, start an Agent Run on that Work Item,
and then read the file the agent committed. All seventeen screens were walked
with the console open; every request answered 200 except reads of a
repository that genuinely had no commits yet.

It found six defects that no API-level test could have:

- The application had **no way to create anything** — every create endpoint
  existed and no screen offered a form, so it could show the platform and
  never add to it.
- Creating an organization took the whole application to a **blank page**: the
  members list returns "username" and this client read "name". Four more of
  the same followed, because the types had been written from the design rather
  than from the handlers.
- A **branch with a slash in it could not be browsed**, which is every branch
  an agent writes (`agents/<key>/work`).
- The **file viewer asked for JSON and got a file** — the blob endpoint serves
  raw bytes.
- The agent list showed **"running" for an agent that was merely enabled**.
- An **agent's own comment was attributed to a person**. A run presents a
  service token because it outlives the request that started it, and the
  interceptor classified every service token as the platform — so the one
  distinction `author_kind` exists to make was wrong for every agent.
- Two of the mismatches were the API's own fault: `WorkItemJSON` and `RunJSON`
  carried only descriptive fields, leaving every listing with no stable key,
  nothing to order by, and no way to say who held an item.

An ErrorBoundary now contains a screen's crash to that screen; the blank page
is what made the first of these hard to see at all.

It also found the largest gap: **the interface was read-only in all but a few
places.** Sixteen endpoints existed with nothing calling them, and the core
object — a Work Item — had no detail screen. That has been closed: a Work Item
now has a page showing what is asked of it, its subtasks, and its discussion
(where a person corrects an agent and an agent records what it decided), and
the interface can decompose an epic, run CI, submit a review verdict, create a
branch, assign work, add a member, add an SSH key, mint a token and enable
two-factor. Four of those needed edge routes that were never written.

## Known limitations

These are real and are not worked around:

- **The model gateway needs a credential this repository does not carry.**
  `hack/env.local.sh` is untracked and holds `AI_API_KEY` and
  `REGISTRY_PASSWORD`; `hack/deploy.sh` refuses to deploy without the first,
  because a deployment with no gateway credential looks configured and is not.
  A fresh clone must create that file before deploying.
- **Model choice is not free on this cluster.** The 27B dense model takes over
  120 seconds to first token for a planner-sized prompt, which is past the
  gateway's upstream header timeout, so every such call 502s. The chart points
  at the MoE model (`qwen3-6-35b-a3b`), which answers the same prompt in about
  six seconds.
- **Verification is a model's judgement.** A run is judged against its
  acceptance criteria by the model, shown only the run's tool calls and their
  results — not its reasoning or its claims — but it is still a model deciding.
  A Work Item with no acceptance criteria is not verified, and its run record
  says so.
- **An agent CI job needs a person behind the push.** Its run is sponsored by
  the member who pushed or triggered CI. A commit pushed by an agent or a
  service has nobody, and its agent jobs fail saying so.
- **The workspace has no network.** `workspace.run` can build and test code
  whose dependencies are vendored or in the standard library; anything that
  downloads modules fails inside the workspace, by design of its network policy.
- **The code index describes the default branch only, and edges only for Go.**
  A push to any other branch is not indexed, so search, the graph and agent
  context answer for the default branch. Python, Java and TypeScript files get
  symbols and chunks but no dependency, test or history edges. Go references
  resolve by package directory and name, so a method call resolves to every
  method of that name in the caller's and its imports' packages. A push of
  more than 50 commits attributes changed files only to the 50 newest; a file
  none of them touched gets no history rather than an invented one.
  `graph_test.sh` passed on revision 70 for the implemented Go graph.
- **Recorded knowledge reaches a run by relevance to its Work Item.** Entries
  are found by English full-text match and, where an embedding model answers,
  by meaning (similarity at least 0.5). A decision sharing none of the Work
  Item's words and carrying no embedding is not recalled. The recall path is
  proven against real services with a model double, not on the cluster.
- **A repository's agent cost budget cannot trip on this cluster**, since no
  model is priced; its token and wall-clock budgets apply.
- **Only Streamable HTTP external MCP servers reach agents.** agent-runtime
  offers an organization's approved servers' tools to every run
  (`mcp.<server>.<tool>`, audited, re-checked against the register on each
  call). An approved **stdio** server is skipped and logged: it is a command
  line, and running an organization-supplied command inside agent-runtime would
  hand it that service's cluster credentials. The register carries no
  per-server credential, so a server needing a bearer token cannot be used yet.
- **There is no deploy action.** Section 14's deploy-to-staging and
  deploy-to-production paths exist only as `approvals.Decide` rules; nothing
  deploys, so nothing follows them.
- **A brokered credential is short-lived only as a lease.** The lease that
  hands a job its secret is single-use and expires, but the value it carries is
  the stored secret itself, not a rotated or scoped credential; a job that
  leaks it leaks the real thing. Production values go to a production job on
  the default branch, and any member can push to the default branch directly.
- **Dependency detection reads four manifest kinds.** go.mod, package.json,
  requirements*.txt and Cargo.toml; a dependency added any other way is not
  seen by the approval policy. The dependencies gate runs osv-scanner, which
  needs network access to its advisory database.
- **Run proof can be written by any member.** `RecordProof` checks the
  organization, not the caller, so an `approval/...` proof row is display, not
  authority: `MayMerge` reads the approvals store, never the proof.
- **A push to an agent branch depends on agent-runtime.** git-platform asks
  agent-runtime whether a run holds a ref under `agents/` before accepting a
  push (or a CreateBranch/CreateCommit) there, and refuses when it cannot get
  an answer. Pushes anywhere else never ask. git-platform now requires
  `AGENTS_ADDR`.
- **No cost limit can be set on this cluster.** The chart prices no model
  (`ai.modelPrices` is empty: a self-hosted model has no list price, and none
  is invented), so StartRun refuses `cost_limit_micros`. Runs are bounded by
  wall clock and tokens.
- **An orphaned run holds its branch for up to its wall-clock limit plus 15
  minutes.** A run whose replica died is only recognisable once no loop could
  still be executing it.
- **Workspace cleanup is bounded by the run credential lifetime.** The
  controller now records an expiry in the namespace; the age-based reaper
  preserves it until then (configured wall-clock limit plus 15 minutes, or
  12 hours when no limit is configured). Legacy namespaces without that
  annotation still use the one-hour orphan fallback. Regression tests passed;
  deployed at revision 71 and exercised by the agent acceptance suite.

## Graph edges, knowledge recall and repository configuration (2026-09-15)

Four gaps, each a seam with a tested component on either side:

- **The indexer wrote no edges.** Parse's references were discarded, so
  dependents, covering tests and change history answered empty on every real
  repository. Go imports, calls and qualified types now become `depends_on`
  and `tested_by` edges, and each changed symbol gains a `changed_by` edge to
  the newest commit that touched its lines, naming the Work Item its message
  or merged agent branch carries. References are kept by name, so re-indexing
  either end re-derives the edge and a stale one cannot survive.
  `ReplaceFileSubgraph` also deleted same-path symbols in every repository of
  the organization.
- **The index followed every branch and missed merges.** It now follows only
  the default branch, and Merge, CreateCommit and CreateBranch publish the
  same push event a transport push does.
- **No run received context, and no agent could record a decision.** Every run
  now goes through `agentrun.Runner`, which assembles context for the Work
  Item before the first turn; `knowledge.record` stores a decision that a
  later related run's brief carries. Context assembly's history signal called
  git-platform with no credential and was refused on every call.
- **`.novaforge/agents` governed nothing.** The agent definition now decides
  the tools offered and callable, the model and the budget; project.yaml and
  `.novaforge/context` are in the brief; a definition that does not parse, or
  carries an undeclared key, fails the run with the error as its summary.
  Deleting project.yaml used to discard every agent definition as well.

The Graph screen answers for a symbol or a file from these edges; the
Knowledge screen lists entries, says who recorded them, and lets a person
record one.

## A log that was not live, and the fix that made it worse (2026-09-15)

On revision 64 the `work_ci` suite failed at exactly the step that proves
liveness: the job prints a line, sleeps thirty seconds, prints another; the
suite reads the log while the job still reports running, and fails if it finds
the job's last line. Two things had stacked. The inotify fix made the follow
hold its latest line back, so a print-then-sleep job's only line during its
running life was never forwarded at all — the live log was empty — and the
whole log arrived in one burst at job end, racing the status flip to success;
that burst was what the step read while the job still reported running.

The follow is gone. The runner polls the log without following while the job
runs, and settles the last lines with a settled read once the pod is terminal.
Polling touches no fsnotify watcher, so the inotify defect — the kubelet
ending the follow early and writing its own error into the stream as if the
job had printed it — cannot occur at all. Each poll forwards the lines past
the byte offset the previous poll already forwarded: a line still without its
newline would otherwise go out twice, truncated and then whole. The e2e read
it red on revision 64 before the fix, and pins both sides: a line is readable
while the job runs, and the last line arrives exactly once.

## Live-log polling correction and deployment recovery (2026-09-16)

Helm revision 67 was left pending after an upgrade bypassed the image
preflight. Rolling back to revision 66 completed successfully; all pods became
ready on `b001ca0`. The in-cluster `work_ci` suite still failed its live-log
assertion on that deployment.

The polling implementation selected the first newline, so only the first log
line was forwarded while running. It now selects the last complete newline
and sends only `data[sent:cut]`. The expanded
`TestJobLogIsReadableWhileTheJobRuns` failed on the missing second live line
before the fix and passed with `-race` after it. All images were built at
`1c3c53f` and deployed through `hack/deploy.sh` with its image preflight intact
(Helm revision 69). The in-cluster `work_ci`, `deploy` and `gui` suites passed:
live logs, artifacts, the Git round trip and GUI endpoints are verified there.
The first deploy/gui harness attempt raced deletion of the preceding test
namespace; waiting for deletion before rerunning resolved that harness failure.

`go test -race ./internal/runner ./internal/ci ./internal/svcauth` passed with
`hack/env.sh` sourced. The full `procoder test` reported
`TestSASTFindsWeakCrypto` failing because `NOVAFORGE_SEMGREP_RULES` was unset;
this is not a full-suite green result.

The earlier startup-hang diagnosis was not established: `grpc.NewClient` is
nonblocking, quiet logs and a futex wait do not prove a hang, and repeated
SIGQUIT diagnostics caused process exits (confirmed in the previous container
log). The health endpoint returned HTTP 200. Commits `9b95e02` and `02b87b0`
therefore must not be treated as proven fixes for this live-log regression.

## Spec traceability

`.procoder/specs/traceability.yaml` maps each of the spec's 33 acceptance
criteria to the tests and e2e steps that actually prove it. Only
`TestAgentCIJob` exists under the name the spec gives. The mapping was decided
by reading test bodies, not by matching names.
`internal/spectrace`'s `TestSpecTraceability` fails when a criterion is missing
from the map, when the map names a criterion the spec lacks, when a cited Go
test is renamed or deleted, or when a cited e2e script or step no longer exists.

**32 covered, 1 partial, 0 uncovered.**

S-1 `TestPATGitClone`, S-2 `TestRepoBrowseAPI`, S-3 `TestAgentBranchScopeEnforced`
and `TestCrossOrgAccessDenied`, S-13 `TestMCPServerOperations` and S-21
`TestCLIFullLifecycle` became covered on 2026-09-15, on `internal/platformtest`:
every service's real gRPC server behind the real interceptor, on the dev
datastores, from credentials identity issues. `cli_test.sh` and
`crossorg_test.sh` were written for them and have **not yet been run on the
cluster**.

Writing them found, and fixed:

- **SECURITY: the CI runner protocol authenticated nobody.** `Register` took the
  organization from the request with no credential, so anything that could
  reach ci-runner (CI job pods can) could enrol a runner into any organization,
  be dispatched its jobs and receive each job's 30-minute clone credential for
  that organization. The token `Register` returned was never checked:
  `ConnectRequest` did not carry it, and `ReportStatus`, log chunks and
  `UploadArtifact` accepted any job id. Registration now needs an owner/admin
  or a platform credential for the organization, and every later call the
  registration token.
- **SECURITY: `StartRun` trusted `agent_id` and `sponsor_id`.** A member could
  issue a grant in their organization to another organization's agent, and
  name anyone as sponsor. `StreamRunEvents` filtered a shared stream by run id
  alone (unreachable today: agent-runtime has no stream interceptor).
- **An agent's credential meant different things on different surfaces.**
  git-platform's own interceptor called an agent run a "service" with no actor:
  its pushes were refused inside its grant, while `CreateCommit`, `CreateBranch`
  and `Merge` let the same credential write main. The credential now names the
  agent and every surface applies its grant.
- **Every ref was reported as kind "commit"**, and an annotated tag as its
  tag-object sha, which no tree or history lookup can use.

No criterion is uncovered: every component the spec names now has a production
caller.

Partial (the map's `note` says exactly what is missing):

- S-9 `TestAirGappedAgentRun`: Runs complete against the cluster's FastLLM-served model (agent_test, agent_ci_test), but "no network egress to any hosted provider" is asserted nowhere: the chart has no egress NetworkPolicy for the services and nothing checks the proxy does not forward upstream.
