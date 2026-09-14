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
- **An agent's files are staged in the service, not in its workspace pod.**
  The per-run namespace carries the run's network policy and resource quota and
  is torn down with the run, but `workspace.write_file` stages into a per-run
  directory inside agent-runtime rather than into that pod. Nothing executes
  those files — they are the content of the commit the run makes through
  git-platform, and running a repository's own code is CI's job, in a per-job
  pod. Moving the staging into the workspace pod is real work that has not been
  done.
- **A run's "succeeded" state means the model stopped asking for tools**, not
  that the Work Item was satisfied. `tests/e2e/agent_test.sh` therefore checks
  for the commit rather than trusting the state; nothing yet judges whether the
  work actually meets the item's acceptance criteria.
- **Semantic search is fixed in code but not yet proven on the cluster.**
  Reading the pipeline end to end found that it could not have worked: the
  indexer read repositories with no credential and git-platform refused it; a
  repository's first push (all-zero old SHA) could not be diffed and was retried
  forever; the embedder sent no gateway credential; the schema stored 768-wide
  vectors while `bge-m3` answers with 1024, so every chunk was refused; and no
  edge route reached `SearchCode` at all. Each is fixed with a test seen red
  first, the Graph screen has a code search box, and `tests/e2e/search_test.sh`
  asserts a query sharing no word with the code ranks it first — but that
  script has not been run against the cluster. The width change is inferred
  from `bge-m3` being the served embedding model; engineering-graph now probes
  the model at startup and refuses to start if the width does not match.
- **The code index follows every branch and misses merges.** A push to any
  branch replaces the indexed content of the files it touches, so a feature
  branch overwrites what the default branch says. Merges made through the API
  and commits made by the `git.commit` tool publish no push event, so neither
  the indexer nor CI sees them.
- **Approved external MCP servers are recorded, not yet consumed.** mcp-server
  keeps each organization's register (request, approve or reject, revoke,
  owner/admin only) and the MCP screen operates it, but nothing offers an
  external MCP server to an agent: `internal/mcp.Client` exists and is called
  only by its tests, and no agent run reads `.novaforge/mcp/` or the register.
  Approving a server therefore changes nothing an agent can do until that
  consumer is written, and it must read the approved list when it is.
- **Gate toggles and the MCP register are tested against real git and
  PostgreSQL but not yet on the cluster.** Neither has been deployed; the
  gui_test.sh checks for their list endpoints have not run.

## Spec traceability

`.procoder/specs/traceability.yaml` maps each of the spec's 33 acceptance
criteria to the tests and e2e steps that actually prove it. Only
`TestAgentCIJob` exists under the name the spec gives. The mapping was decided
by reading test bodies, not by matching names.
`internal/spectrace`'s `TestSpecTraceability` fails when a criterion is missing
from the map, when the map names a criterion the spec lacks, when a cited Go
test is renamed or deleted, or when a cited e2e script or step no longer exists.

**7 covered, 21 partial, 5 uncovered.**

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
- S-4 `TestWorkItemLifecycle`: acceptance criteria, constraints, required gates
  and assignment to a human are never asserted.
- S-5 `TestEngineeringRunProof`: plan, change impact and the producing
  agent/model are never exposed by a run in any test.
- S-6 `TestRunnerJobStreamAndArtifact`: logs are never read while a job runs,
  and artifact content has no download route.
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
- S-20 `TestMaintenanceProposesWorkItem`: the production sweep from scanner to
  proposal is untested.
- S-21 `TestCLIFullLifecycle`: `nf run gates` and `nf run merge` are never
  exercised.
- S-22 `TestHelmDeploy`: the expected set of services is never checked.
