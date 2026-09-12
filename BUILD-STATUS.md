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
- **Agent workspace isolation is implemented but not exercised in anger.**
  CI jobs run in their own Kubernetes pod, proven by test. Agent Run namespaces
  are implemented and unit-tested against the client-go fake; a full agent run
  through the tool loop against the live model has not been driven end to end.
- **Embeddings are configured but not proven end to end.** `embed`/`bge-m3`
  answer on the gateway (verified by hand), and the indexing path is tested
  against the real pgvector database, but no acceptance test drives a push all
  the way through to a semantic search result.
