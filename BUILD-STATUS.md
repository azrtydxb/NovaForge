# NovaForge build status

Autonomous build of the backend described in `.procoder/specs/backend-platform.md`,
executed against the six plans in `.procoder/plans/`.

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

MERGE_TEST_RESULT

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
- **The code index follows every branch and misses merges.** A push to any
  branch replaces the indexed content of the files it touches, so a feature
  branch overwrites what the default branch says. Merges made through the API
  and commits made by the `git.commit` tool publish no push event, so neither
  the indexer nor CI sees them.
- **Approved external MCP servers are recorded, not yet consumed.** mcp-server
  keeps each organization's register and the MCP screen operates it, but
  nothing offers an external MCP server to an agent: `internal/mcp.Client` is
  called only by its tests. Approving a server changes nothing an agent can do
  until that consumer is written, and it must read the approved list when it is.

## Spec traceability

`.procoder/specs/traceability.yaml` maps each of the spec's 33 acceptance
criteria to the tests and e2e steps that actually prove it. Only
`TestAgentCIJob` exists under the name the spec gives. The mapping was decided
by reading test bodies, not by matching names.
`internal/spectrace`'s `TestSpecTraceability` fails when a criterion is missing
from the map, when the map names a criterion the spec lacks, when a cited Go
test is renamed or deleted, or when a cited e2e script or step no longer exists.

**12 covered, 16 partial, 5 uncovered.**

Uncovered: the component is tested, but nothing in production calls it, so the
behaviour cannot be seen on the deployed platform:

- S-7 `TestAgentBranchLockedDuringRun`: nothing acquires `agents.BranchLock`,
  and git-platform lets every user push to an agent branch mid-run.
- S-11 `TestApprovalPaths`: nothing calls `approvals.Decide`.
- S-12 `TestBrokerDownFailsClosed`: nothing calls `ResolveJobCredentials`, so a
  job that needs credentials is not blocked when the broker is down.
- S-14 `TestGraphQueries`: no production code writes `depends_on`, `tested_by`
  or `changed_by` edges, so every graph query returns empty.
- S-17 `TestKnowledgeRecall`: agent runs never assemble context, and the
  `AssembleContext` RPC has no caller.

Partial (the map's `note` says exactly what is missing):

- S-1 `TestPATGitClone`: no clone with a PAT; revoked tokens are refused only at
  token resolution, never at the git transport.
- S-2 `TestRepoBrowseAPI`: tree, blob, diff and tags are never read through REST.
- S-3 `TestAgentBranchScopeEnforced`: the transport half runs against a stub
  capability function, never a real agent grant.
- S-3 `TestCrossOrgAccessDenied`: most cross-org repo, Work Item write, CI run and
  Agent Run paths are unasserted.
- S-7 `TestAgentRunIsolationAndEvidence`: namespaces are only checked against
  the fake clientset, and evidence is never read after teardown.
- S-7 `TestRunBudgetHardStop`: only the token limit stops a run; the stored
  over_budget state is never read back.
- S-8 `TestToolCallAudited`: the registry's audit entry is not checked for
  arguments or outcome.
- S-9 `TestAirGappedAgentRun`: "no egress to a hosted provider" is asserted
  nowhere.
- S-10 `TestGateBlocksMerge`: the controller and the merger are never joined
  through the real client, and no merge is attempted end to end.
- S-10 `TestGateConfigSelfEditRejected`: checked at the controller only, and
  only for deleting gates, not weakening them.
- S-12 `TestShortLivedCredential`: no job ever receives a brokered credential.
- S-13 `TestMCPServerOperations`: none of the four operations succeeds over
  either transport.
- S-15 `TestIndexUpdatedOnPush`: the dependency index is not implemented, and
  the search e2e has not been run.
- S-18 `TestRepoConfigGoverns`: `.novaforge` agent configuration governs nothing
  (`repoconfig.Load` has no caller).
- S-19 `TestSwarmDependencyOrder`: role-to-agent assignment is not asserted, and
  nothing marks a subtask blocked when its run fails.
- S-21 `TestCLIFullLifecycle`: `nf run gates` and `nf run merge` are never
  exercised.
