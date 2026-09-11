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

31 Go packages pass `go test`, 0 failures, against the real PostgreSQL, Redis
and MinIO — no datastore is mocked anywhere.

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

## Known limitations

These are real and are not worked around:

- **No model is available in the cluster.** The FastLLM proxy at
  `192.168.10.125:4000` authenticates but `/v1/models` returns an empty list.
  Everything that needs an LLM — the agent run loop, epic decomposition, agent
  reviewers, semantic embeddings — is implemented and unit-tested against
  in-process stub models, which is the correct double for an external service,
  but **has not been exercised against a real model**. Point `ai.endpoint` at a
  served model and those paths become testable.
- **CI job isolation is real, agent workspace isolation is untested in anger.**
  CI jobs run in their own Kubernetes pod, proven by test. Agent Run namespaces
  are implemented and unit-tested against the client-go fake, but a full agent
  run needs a model (above).
- **Some typed agent tools report an honest error rather than working**:
  `repo.search`, `repo.get_symbol`, `repo.get_dependencies` need the graph
  client wired into the tool adapter, and `git.commit` and `work.comment` need
  RPCs that do not exist yet on git-platform and work. They fail loudly with the
  reason instead of returning a plausible empty result.
