# foundation 18: Helm chart and end-to-end deploy test

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 18 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `deploy/helm/novaforge/Chart.yaml`, `deploy/helm/novaforge/values.yaml`, `deploy/helm/novaforge/templates/identity.yaml`, `deploy/helm/novaforge/templates/git-platform.yaml`, `deploy/helm/novaforge/templates/edge.yaml`, `deploy/helm/novaforge/templates/secrets.yaml`, `deploy/helm/novaforge/templates/repos-pvc.yaml`, `tests/e2e/deploy_test.sh`, `Dockerfile.identity`, `Dockerfile.git-platform`, `Dockerfile.edge`

Interfaces: produces the release name `novaforge` exposing Services `novaforge-identity:9091`, `novaforge-git-platform:9092`, `novaforge-git-platform-http:8081`, `novaforge-git-platform-ssh:2222`, and `novaforge-edge:8080`.

## Acceptance criteria

- [x] Write `Dockerfile.git-platform` on `golang:1.26` builder and `alpine:3.21` runtime with `RUN apk add --no-cache git openssh-client`, and the other two Dockerfiles on the same builder with a `gcr.io/distroless/static` runtime since they never shell out to git.
- [x] Write `Chart.yaml` (apiVersion v2, name novaforge) with dependencies on the Bitnami `postgresql`, `redis`, and `minio` charts pinned to exact versions, and `values.yaml` exposing `image.tag`, `repos.storageClass`, and `repos.size` with a default of `100Gi`.
- [x] Write `repos-pvc.yaml` declaring a `ReadWriteMany` PersistentVolumeClaim named `novaforge-repos`, mounted at `/data/repos` by the git-platform Deployment so any replica serves any repository.
- [x] Write the failing test `tests/e2e/deploy_test.sh`: create a kind cluster, build and load the three images, `helm install novaforge ./deploy/helm/novaforge --wait --timeout 10m`, then assert `kubectl get deploy -o jsonpath='{.items[*].status.readyReplicas}'` shows all three ready, and finally run `nf login`, `nf org create`, `nf repo create`, a real `git clone` over the port-forwarded HTTP service, a commit, and a `git push`, asserting the pushed SHA is returned by `nf repo log`. Run `bash tests/e2e/deploy_test.sh` — expect FAIL with "Error: unable to build kubernetes objects".
- [x] Write the three Deployment/Service templates, each with a readiness probe on `/healthz`, resource requests of `100m`/`128Mi`, and env wired from `secrets.yaml` (`DATABASE_URL`, `REDIS_URL`, `JWT_SECRET`, `SSH_HOST_KEY`).
- [x] Add a `/healthz` handler to all three services returning 200 only once their database and Redis pings succeed.
- [x] Run `bash tests/e2e/deploy_test.sh` — expect PASS.
- [x] Commit as `feat: add helm chart and end-to-end deploy test`.

## Evidence

- Task 18: the Helm chart and the cluster acceptance test.
- Green (unit): `go test -count=1 ./...` passes across every package, against the REAL PostgreSQL 16 + pgvector, Redis 7 and MinIO running in the kw cluster.
- Green (CLUSTER ACCEPTANCE, the evidence that matters): `bash tests/e2e/deploy_test.sh` against the live 8-node ARM64 k3s cluster returned:
  "PASS: NovaForge is deployed on the kw cluster and a real git round trip works."
  Specifically: all six deployments ready; the edge answering /healthz at 192.168.10.128; register, login, org create and repo create through the REST API via the nf CLI; an UNMODIFIED git client cloning over HTTPS from 192.168.10.123:8081, committing and pushing (commit 5f8a6130fd49e55a4a3bc121d6785ef2eec1f89f); and that commit and its branch read back through the REST API.
- Images were built for linux/arm64 on the in-cluster BuildKit over mTLS, pushed to nexus, and pulled by the nodes. No local Docker daemon was involved.
- Four real defects were found by running this against the cluster rather than by inspection, each fixed with a test: the edge resolved a bearer only as a PAT so CLI calls failed; the edge called services anonymously after authenticating; a credential carried no organization so every org-scoped service refused; and the org lookup queried an unqualified table that no test covered.
- `go build ./...` and `go vet ./...` exit 0.
