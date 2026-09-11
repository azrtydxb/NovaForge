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

## Integration proven so far

- An unmodified `git` client clones, commits and pushes over **both HTTPS and SSH**,
  and both transports refuse an out-of-scope ref identically.
- Push events reach a real Redis consumer group.
- An arm64 image built from this source ran as a pod in the cluster.
