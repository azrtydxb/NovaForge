# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Current state

The backend is built, deployed to the kw cluster, and covered by five acceptance tests that
run against it. The GUI is built too, from the claude.ai/design project "NovaForge GUI
design": `web/` is a React + TypeScript + Vite application that the edge embeds and serves,
so a deployment cannot serve an API with no UI, or a UI built from a different commit than
the API it talks to.

`NovaForge_AI_Native_Git_Platform.md` is the design document and remains the source of
truth for intent. `.procoder/specs/backend-platform.md` is the spec actually built against,
`.procoder/plans/` holds the six implementation plans, and `BUILD-STATUS.md` records what
has been proven on the cluster, what defects running it exposed, and what is still not
proven. **Read `BUILD-STATUS.md` before claiming anything works** — its "Known limitations"
section is deliberately honest and kept current.

## Commands

```bash
make test          # go test ./...  — needs the datastore env below, or most suites skip
make build lint    # go build ./... / go vet ./...
make generate      # buf generate — regenerate gen/ after editing proto/
make openapi       # regenerate api/openapi.yaml from the edge route table

go test ./internal/work/ -run TestAddAndListComments -v   # one test
gofmt -l internal cmd                                      # must print nothing

cd web && npm ci && npm run build   # the app the edge embeds
cd web && npx tsc -b --noEmit       # type-check without building
```

The web application is embedded into the edge binary at image build time
(`deploy/docker/Dockerfile.edge`, the only image with a Node stage). A plain `go build`
outside that image has an empty `internal/edge/dist/`, and the edge then says so on every
page rather than serving a blank one.

Tests run against **real** PostgreSQL, Redis and MinIO — no datastore is mocked anywhere.
`source hack/env.sh` exports `TEST_DATABASE_URL`, `TEST_REDIS_URL` and the S3 variables
pointing at the cluster's dev datastores. Without them the suites `t.Skip`, which looks
like a pass; always source it before believing a green run.

### Cluster build and deploy

There is no local Docker daemon — Docker Desktop's containerd store is broken and unusable.
Everything builds in-cluster.

```bash
source hack/env.sh          # BuildKit, registry, kube context, test datastores
./hack/build-images.sh      # arm64 images via in-cluster BuildKit, tagged with the commit sha
./hack/deploy.sh            # helm upgrade --install, with an image preflight
bash tests/e2e/deploy_test.sh     # git round trip over HTTPS and SSH
bash tests/e2e/work_ci_test.sh    # Work Item, push, CI run in a pod, log and artifact
bash tests/e2e/factory_test.sh    # epic decomposition by the real model, dependency ordering
bash tests/e2e/agent_test.sh      # an Agent Run executes and commits its work
bash tests/e2e/agent_ci_test.sh   # a CI job with an agent role runs as an Agent Run
bash tests/e2e/merge_test.sh      # independent review, then a merge that lands on main
bash tests/e2e/gui_test.sh        # the app is served and every screen's endpoint answers
bash tests/e2e/search_test.sh     # a push is indexed and found by meaning, not by keyword
```

`hack/env.local.sh` is untracked and holds `REGISTRY_PASSWORD` and `AI_API_KEY`. A fresh
clone must create it: `deploy.sh` refuses to run without the model-gateway credential,
because a deployment that cannot reach a model looks configured and is not.

Images are tagged with the commit sha, never a mutable tag — a mutable `dev` tag with
`IfNotPresent` silently served stale code for a whole session. **Never `kubectl set image`
or otherwise edit a resource Helm owns**: it takes server-side-apply field ownership and
the next `helm upgrade` conflicts.

## Architecture

Go microservices, module `github.com/novaforge/novaforge`. gRPC between services (buf,
STANDARD lint), REST/OpenAPI only at the edge, Redis Streams for events.

**Frontend** — `web/`, React 19 + TypeScript + Vite. One API client (`src/lib/api.ts`), one
workspace scope every screen reads (`src/lib/workspace.tsx`: an organization and optionally
one repository — "all projects" never crosses an organization), design tokens in
`src/theme.css`, one file per screen under `src/screens/`. Where a deployment has no
endpoint for something a screen shows, the screen says so: `Failed` distinguishes "not
available in this deployment" from a request that actually failed. **Never render invented
data** — a GUI showing a plausible number for something the platform does not know is worse
than one that admits the gap.

**Services** (`cmd/`, each with a matching slice of `internal/`): `identity`,
`git-platform`, `work-reviews`, `ci-runner`, `gates`, `agent-runtime`,
`engineering-graph`, `mcp-server`, `edge`. Plus `runner` (dials out, never dialled) and
`nf` (the CLI).

### Rules that are load-bearing

- **Organizations are a hard security boundary.** Every query carries an org predicate
  taken from `authz.FromContext` — never from a request field, or a caller could name
  another org and be believed. The two exceptions (`work.Store.OpenEpics`,
  `OrganizationsWithWork`) are platform-worker queries that return only ids and re-enter
  each org's scope before reading anything; both say so in their doc comments.
- **One PostgreSQL cluster, one schema per service, no cross-schema reads.** A service
  needing another's data calls its RPC. `database.Migrate(url, schema, fs)` owns this.
- **Capability grants constrain agents, not human members.** A member has ordinary write
  access to their own repositories; demanding a grant of them is a bug, and was one.
- **Git is the `git` binary**, shelled out to. Never go-git. Write operations happen in a
  throwaway clone that is pushed back, so a failure part-way leaves the bare repo untouched.
- **All model work goes through `go-ai-sdk`'s gateway provider.** No provider-specific logic
  anywhere; a model server's quirks are configuration (`AI_PROVIDER_OPTIONS`), never a
  branch in the code.
- **Store observable actions and evidence, never model chain-of-thought.**
- MCP: current spec revision only (`2025-06-18`), stdio + Streamable HTTP, no deprecated
  HTTP+SSE.

### The defect class to watch for

Almost every defect found here was a **seam**, not a component: both sides were correct and
unit-tested, and nothing connected them. The in-process doubles are where this hides: the
client-go fake validates no Kubernetes schema, so a namespace with an illegal label and a
pod with an empty PVC claim both passed every unit test and were refused by the API server.
Assert against the real validator (`k8s.io/apimachinery/pkg/util/validation`) rather than
against the fake's tolerance. `ClaimJob` and `Dispatch` were both written, both
tested, and called by nothing. So were the swarm scheduler, the maintenance scanners and the
auto-merger. Three config fields were declared, rendered into the chart, set in the pod, and
never read. These are invisible to unit tests and usually present as _silence_ — a queued
run that never starts, an event consumed and acked with nothing scheduled — which is
indistinguishable from correct idle behaviour.

When adding anything, check what calls it, end to end, on the cluster. A test proving a
component works is not evidence that anything uses it.

## Working conventions

- Write the failing test first and see it fail for the right reason. For a regression test,
  revert the fix, watch it go red, restore it. A test never seen red proves nothing.
- Comments explain _why_, especially where the obvious approach is wrong — much of this
  codebase's commentary records a defect that was actually hit. Match that density.
- `.procoder/todo/` tracks multi-step work; `launcher.sh todo close` refuses to close a task
  without checked criteria and real evidence. Never edit `Status:` by hand.
- Prefer fixing the design over widening a test's tolerance.

## Commit gate

A procoder hook runs on commit and **blocks** on findings:

- AI-attribution trailers (`Co-Authored-By: Claude …`) are rejected, and the gate inspects
  the whole range, not just `HEAD` — one bad commit anywhere blocks the push.
- Vulnerable dependencies, merge markers, unformatted files, semgrep findings and oversized
  files all block.
- When a turn ends by putting a decision to the user, that decision must be recorded in
  `.procoder/ask/decisions.md` and asked via the structured question tool.

Helm templates live in `deploy/helm/novaforge/templates/*.tpl`, not `.yaml`: Go templates
are not valid YAML and prettier cannot parse them.
