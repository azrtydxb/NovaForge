# foundation 18: Helm chart and end-to-end deploy test

Status: open
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

- [ ] Write `Dockerfile.git-platform` on `golang:1.26` builder and `alpine:3.21` runtime with `RUN apk add --no-cache git openssh-client`, and the other two Dockerfiles on the same builder with a `gcr.io/distroless/static` runtime since they never shell out to git.
- [ ] Write `Chart.yaml` (apiVersion v2, name novaforge) with dependencies on the Bitnami `postgresql`, `redis`, and `minio` charts pinned to exact versions, and `values.yaml` exposing `image.tag`, `repos.storageClass`, and `repos.size` with a default of `100Gi`.
- [ ] Write `repos-pvc.yaml` declaring a `ReadWriteMany` PersistentVolumeClaim named `novaforge-repos`, mounted at `/data/repos` by the git-platform Deployment so any replica serves any repository.
- [ ] Write the failing test `tests/e2e/deploy_test.sh`: create a kind cluster, build and load the three images, `helm install novaforge ./deploy/helm/novaforge --wait --timeout 10m`, then assert `kubectl get deploy -o jsonpath='{.items[*].status.readyReplicas}'` shows all three ready, and finally run `nf login`, `nf org create`, `nf repo create`, a real `git clone` over the port-forwarded HTTP service, a commit, and a `git push`, asserting the pushed SHA is returned by `nf repo log`. Run `bash tests/e2e/deploy_test.sh` — expect FAIL with "Error: unable to build kubernetes objects".
- [ ] Write the three Deployment/Service templates, each with a readiness probe on `/healthz`, resource requests of `100m`/`128Mi`, and env wired from `secrets.yaml` (`DATABASE_URL`, `REDIS_URL`, `JWT_SECRET`, `SSH_HOST_KEY`).
- [ ] Add a `/healthz` handler to all three services returning 200 only once their database and Redis pings succeed.
- [ ] Run `bash tests/e2e/deploy_test.sh` — expect PASS.
- [ ] Commit as `feat: add helm chart and end-to-end deploy test`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
