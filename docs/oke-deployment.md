# NovaForge on the azrty OKE cluster

`deploy/helm/novaforge/values-oke.yaml` deploys NovaForge into namespace `novaforge` as
Helm release `novaforge`, served at `https://novaforge.azrty.com`. Kuvryn Sync renders it
offline, the same way it renders `values-kw.yaml` for kw. Every OKE-specific behaviour in
the chart is a value that is off by default, so kw's render does not change.
`internal/service`'s `TestKwRenderIsUnchanged` holds kw's render to a golden file.

This phase is not proven on the cluster. Nothing has been applied, no image has been
pushed to OCIR, and the namespace does not exist yet. See "What has and has not been
verified" at the end.

## Isolation on OKE

**OKE's CNI is flannel. It enforces no NetworkPolicy, and the cluster has no Cilium.**

NovaForge's isolation guarantees are built on network policy. On kw, Cilium enforces them
and `tests/e2e/airgap_test.sh` proves it:

- Agent Run workspaces, gate analysis sandboxes, semantic-producer sandboxes and CI job
  pods run untrusted repository code. Each one sits behind a deny-all NetworkPolicy that
  the service creates in that pod's namespace.
- The services that call the model are held to the cluster and the model gateway by a
  `CiliumNetworkPolicy` (`networkPolicy.airGapped`).

On OKE the Kubernetes API still accepts a standard NetworkPolicy, but nothing enforces it.
A `CiliumNetworkPolicy` cannot be applied at all, because the kind does not exist. So
**none of those guarantees holds on OKE**. Code a repository supplies would reach
`shared-pg`, every other tenant's Services, the cloud metadata endpoint and the internet.

`values-oke.yaml` therefore keeps every code-executing feature in the state where the
platform refuses it:

| Value in values-oke.yaml                                                    | Feature that is off                                                                                | How it presents                                                                                                                                                                                                                                      |
| --------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `networkPolicy.enabled: false`                                              | The egress policy for model-calling services. It cannot render on OKE because the kind is unknown. | No network-layer egress control for any NovaForge pod.                                                                                                                                                                                               |
| `services.gates.analysisImage: ""`, `sandboxRbac: false`                    | Executable gates (repository tests and compilers in the sandbox)                                   | gates logs `NF_GATE_ANALYSIS_IMAGE is unset; executable gates are unavailable`, and never falls back to running code in-process. Static gates still run.                                                                                             |
| `services.agent-runtime.automountServiceAccountToken: false`, `rbac: false` | Agent Runs (workspace pods that run commands)                                                      | agent-runtime finds no in-cluster config, so no provisioner is built. `StartRun` returns `FailedPrecondition` ("executor … must be available before admission"). That covers CI jobs with an agent role too. Agents can still be created and listed. |
| `runner.orgId: ""`                                                          | CI runners (repository commands in job pods)                                                       | No runner Deployment. **This is not a clean refusal**: CI runs are accepted and stay queued, because nothing claims them. The platform has no flag that makes ci-runner refuse a run when no runner exists.                                          |
| `operatorConfigs.semanticProducer.secretName: ""`                           | Semantic producer sandboxes                                                                        | The feature reports itself unconfigured.                                                                                                                                                                                                             |
| `operatorConfigs.deployments.secretName: ""`                                | Governed deployments (Helm jobs in pods)                                                           | The feature reports itself unconfigured.                                                                                                                                                                                                             |
| `operatorConfigs.openbao.secretName: ""`                                    | Brokered CI credentials                                                                            | Not wired, and there is no runner to use them.                                                                                                                                                                                                       |
| `factory.autoMerge.enabled` (chart default, `false`)                        | Auto-merge                                                                                         | Off, as everywhere.                                                                                                                                                                                                                                  |

These still work: the GUI and API, organizations and identity, repositories, Git
smart-HTTP and LFS, imports from github.com, Work Items, reviews and merges, static gates,
search and graph (embeddings go through the model gateway), knowledge, and MCP.

The maintenance scanners in work-reviews and gates (gitleaks, osv-scanner, semgrep and
`go list -m -u`) read repository contents in their own pods and do not execute it. They
stay on.

These are the ways to turn the code-executing features back on. Each one is a platform
decision, not a chart setting:

1. **Install a policy engine cluster-wide**, either Calico in policy-only mode alongside
   flannel or Cilium in CNI chaining mode. Standard NetworkPolicy is then enforced. The
   chart's `CiliumNetworkPolicy` (`networkPolicy.enabled`) needs Cilium specifically.
2. **Run code-executing workloads on a separate node pool or cluster that does enforce
   policy**, and point the runners and sandboxes there.
3. **Keep them off on OKE**, as `values-oke.yaml` does now.

Whichever you choose, re-enabling also needs the cluster-scoped RBAC described under
"RBAC" below.

## Images

CRI-O on OKE runs in short-name enforcing mode, so every image is fully qualified:

- The services are `dxb.ocir.io/axmcgpqk0w0z/azrty/novaforge-<svc>:<tag>` (`image.registry`,
  `image.repository`, and the new `image.namePrefix: novaforge-`). They are pulled with
  `imagePullSecrets: [ocir-pull]`.
- The one bundled datastore is `docker.io/library/redis:8`, pulled from Docker Hub
  anonymously (subject to its pull rate limit; mirror it into OCIR if that becomes a
  problem). There is no bundled MinIO on OKE: MinIO no longer publishes community images,
  and `docker.io/minio/minio` refuses pulls, so the first rollout's MinIO pod never started.

The nodes are arm64 (VM.Standard.A1.Flex), which matches what `hack/build-images.sh`
builds. To build for OCIR:

```bash
source hack/env.sh
# DOCKER_CONFIG must hold an OCIR credential: BuildKit pushes with what it finds there.
NF_REGISTRY_PUSH=dxb.ocir.io NF_REGISTRY_PULL=dxb.ocir.io \
NF_REGISTRY_REPO=axmcgpqk0w0z/azrty NF_IMAGE_PREFIX=novaforge- ./hack/build-images.sh
```

Then set `image.tag` in `values-oke.yaml` to the commit tag that was pushed. The tag in the
file today names a commit that has not been pushed to OCIR. The OCIR repositories must
exist, or the tenancy must allow them to be created on push. There is no
`promote-oke.sh` yet. `hack/promote-kw.sh` checks kw's Nexus and nothing else.

## Routing: one host for the app, Git and LFS

`gateway.enabled` renders one HTTPRoute (`templates/httproute.tpl`) attached to Gateway
`edge` in namespace `envoy-gateway-system`, listener `novaforge-https`, host
`novaforge.azrty.com`. TLS is terminated at the Gateway, and both backends are plain
HTTP:

| Order | Match                                                                                                                                           | Backend                       | Timeout                |
| ----- | ----------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------- | ---------------------- |
| 1     | `RegularExpression` `^/[^/]+/[^/]+\.git/(info/refs\|git-upload-pack\|git-receive-pack\|info/lfs/objects/batch\|info/lfs/objects/[0-9a-f]{64})$` | `novaforge-git-platform:8081` | `request: 1h`          |
| 2     | `PathPrefix` `/`                                                                                                                                | `novaforge-edge:8080`         | implementation default |

- The expression mirrors `internal/gitops`' router: `/{org}/{repo}.git/` followed by one of
  the operations `httpHandler.ServeHTTP` serves (`info/refs`, `git-upload-pack`,
  `git-receive-pack`, the LFS batch endpoint and LFS object transfers). The path is matched
  without the query string, so `info/refs?service=…` matches.
- `RegularExpression` is in the installed HTTPRoute CRD (Gateway API bundle v1.6.1,
  standard channel) as implementation-specific support. Envoy Gateway v1.9.2 implements it
  as Envoy `safe_regex` (RE2, whole-path match). `kuvryn-pulse`'s route on this cluster
  already uses one and is `Accepted`. Envoy Gateway ranks a regex match above a
  `PathPrefix` whatever the order. The git rule is listed first anyway.
- The git rule's `request: 1h` timeout exists because Envoy's default route timeout is
  15s, which a large clone or push exceeds.
- git-lfs is sent back over https because Envoy sets `X-Forwarded-Proto: https`, which
  `lfsScheme` reads first.
- `lfs.publicURL: https://novaforge.azrty.com` is rendered as `NF_GIT_PUBLIC_URL`. It is the
  only clone/LFS origin setting the chart has. ci-runner clones in-cluster through
  `http://novaforge-git-platform:8081`, its built-in default, which is correct for release
  `novaforge`.
- **Git over SSH is not exposed on OKE in this phase.** git-platform still listens on 2222
  inside the cluster, but no Service type or route reaches it from outside. SSH-issued LFS
  tokens (`Bearer nflfs_…`) need a TLS connection to git-platform itself, so they could not
  work through this route anyway. LFS works with HTTPS basic authentication.
- `gitTLS.enabled: false`. There is no `cluster-ca` ClusterIssuer on OKE, and the Gateway
  carries TLS. The `edge` and `git-platform` Services are `ClusterIP`
  (`extraServiceType: ""`), so no public OCI load balancer is created beside the Gateway.

The platform team creates the `novaforge-https` listener with the certificate for
`novaforge.azrty.com`, restricted to namespace `novaforge`. DNS for that name must point at
the Gateway.

## Database: shared CNPG cluster

`externalDatabase.existingSecret: novaforge-database` (key `uri`) does two things:

- The bundled PostgreSQL (its claim, Deployment and Service) is not rendered.
- Every service gets `DATABASE_URL` from that key, which overrides any `DATABASE_URL` in
  `novaforge-secrets`.

Each service still runs its own migrations into its own schema (`database.Migrate`
creates the schema and connects with `search_path=<schema>`).

What the platform must provide on `shared-pg`:

- A `Database` `novaforge` owned by a login role `novaforge`. As owner, the role has
  `CREATE` on the database, and each service needs that to create its schema: identity,
  gitplatform, work, ci, gates, agents, graph, mcp and edge.
- **The `vector` extension, created by the platform.** `internal/graph` and
  `internal/knowledge` run `CREATE EXTENSION IF NOT EXISTS vector SCHEMA public`. pgvector
  is not a trusted extension, so a non-superuser owner cannot create it, but `IF NOT
EXISTS` succeeds when it is already there. Declare it on the `Database` resource, as
  `shared-pg-novamem` and `shared-pg-pulse` do:
  `extensions: [{name: vector, ensure: present}]`. It must be installed into `public`.
  The migrations reference `public.vector`. The embedding columns are `vector(768)`, so the
  `embed` model behind the gateway must return 768 dimensions.
- **Do not pre-create `citext`.** Identity's migration creates it with no `SCHEMA`, which
  puts it in the `identity` schema, where identity's `search_path` finds it. citext is a
  trusted extension, so the owner can create it. A platform-created citext in `public`
  would make that `IF NOT EXISTS` a no-op and leave the `citext` type outside identity's
  `search_path`.
- **The DSN must use `shared-pg-rw.shared-pg.svc:5432`, not `shared-pg-pooler-rw`.** That
  pooler is transaction-mode PgBouncer. It rejects `search_path` as a startup parameter and
  breaks session state across a migration. `shared-pg-pooler-session` might work but has
  not been tried.
- The cluster enforces TLS 1.3. pgx's default `sslmode=prefer` negotiates it, so no
  `sslmode` is needed in the URI. `sslmode=require` is fine too.

Example shape (no real credentials):
`postgresql://novaforge:<password>@shared-pg-rw.shared-pg.svc:5432/novaforge`.

## Object storage: OCI Object Storage

`datastores.minio.enabled: false`. Services reach OCI Object Storage's S3 API at the
endpoint in `novaforge-secrets`, with `objectStorage.useSSL: true` and
`objectStorage.region: me-dubai-1` (rendered as `S3_USE_SSL` and `S3_REGION`). The API
answers only over HTTPS and only to requests signed for its own region; before these two
settings existed, every service spoke plain HTTP to `S3_ENDPOINT`. The buckets
`novaforge-ci` and `novaforge-releases` and the `novaforge-objects` user, whose policy is
limited to those two buckets, are in azrty-infra `oci/novaforge.tf`. The policy grants
`read buckets`, not `manage`, so the buckets must exist before the services start:
`blobstore.New` creates a bucket only when it is missing.

## Redis: bundled, on oci-bv

oci-bv is block storage. It is ReadWriteOnce, and its smallest volume is 50Gi (OCI rounds
smaller requests up).

- `repos`: `storageClass: oci-bv`, `size: 50Gi`, and the new `accessMode: ReadWriteOnce`.
  OKE has no ReadWriteMany class, so git-platform runs as one replica with
  `strategy: Recreate`. A surge pod on another node could never attach the volume.
- `datastores.redis.persistence` (new; off by default, so kw's Redis is unchanged): on,
  with `oci-bv` and 50Gi mounted at `/data`, so the event streams survive a reschedule.
  The pod runs as the image's redis user (uid and fsGroup 999): a fresh oci-bv volume is
  root-owned, and the first rollout's Redis could not create its append-only directory.
- `storage.pruneProtection: true` keeps Sync from pruning either claim.

## Model gateway

`ai.endpoint` and `ai.embedEndpoint` are `https://api-ai.watteel.com/v1`, the public
FastLLM proxy. The model names (`qwen3-6-35b-a3b`, `embed`) and `providerOptions` are the
chart defaults, the same as kw. Confirm the public proxy serves those names. The key is
`AI_API_KEY` in `novaforge-secrets`.

This is not air-gapped. `internal/service`'s `TestAirGappedAgentRun` holds the chart
defaults to in-cluster endpoints. That test still passes, because it reads `values.yaml`,
not `values-oke.yaml`. OKE is deliberately outside its claim.

## Egress

There are two separate mechanisms:

- **`networkPolicy` / `airGapped`** renders `CiliumNetworkPolicy` objects that confine pods
  at the network layer. On kw it is the only thing that keeps agent-runtime,
  engineering-graph and the rest inside the cluster. It would also block
  `api-ai.watteel.com`, because `airGapped` admits only cluster entities. On OKE it is off:
  it cannot render, and nothing would enforce it anyway. **So nothing at the network layer
  restricts NovaForge pods on OKE.** They reach `api-ai.watteel.com:443`, github.com and
  `shared-pg-rw.shared-pg:5432` because nothing stops them. The same is true of any other
  destination.
- **`outbound.destinations`** is git-platform's application-layer allowlist for imports,
  mirrors and webhooks, rendered as `NF_OUTBOUND_DESTINATIONS`. `internal/egress` enforces
  it inside the process: exact scheme, host and port; pinned resolution; and loopback,
  link-local and metadata addresses refused, plus private ranges unless a private CIDR is
  listed. It does not depend on the CNI and still holds on OKE. `values-oke.yaml` allows
  `https://github.com:443` (CIDR `0.0.0.0/0` and `::/0`, as on kw: github.com's addresses
  move, and the exact host match does the constraining). An import of a repository whose
  LFS objects live on another GitHub host would need that host listed too.

## RBAC

`rbac.create: false`. The namespace-scoped deployer cannot create cluster-scoped objects,
so the chart renders no ServiceAccount, Role or ClusterRole.

While the code-executing features are off, **NovaForge needs no extra RBAC on OKE**.
agent-runtime runs on the default ServiceAccount with no token, gates has no sandbox
ServiceAccount, and there is no runner. Do not grant the ClusterRoles from
`deploy/kuvryn-sync/runtime-rbac.yaml` (namespace create/delete, pod exec) before
isolation is enforced. Together with the features above they are exactly what lets
repository code run unconfined. When isolation is in place, the platform repo carries
those ServiceAccounts, ClusterRoles and bindings, mirroring `runtime-rbac.yaml` for
namespace `novaforge`. In `values-oke.yaml`, set `services.agent-runtime.rbac: true` and
remove `automountServiceAccountToken: false`, and set `services.gates.sandboxRbac: true`.

The Sync deployer's Role in `novaforge` must cover what the render contains: Services,
PersistentVolumeClaims, Deployments, and `gateway.networking.k8s.io` HTTPRoutes. The last
is not in kw's `deploy/kuvryn-sync/rbac.yaml`, because kw renders none. It does not need
CiliumNetworkPolicies or cert-manager Certificates, because OKE renders neither.

## Secrets the namespace must contain

The platform repo provides all of them through SOPS. The chart references them by name
only and renders none.

| Secret               | Keys                                                                                                                                   | Used by                                                                                                                                                                                                                                              |
| -------------------- | -------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `novaforge-secrets`  | `JWT_SECRET`, `HMAC_SECRET`, `SECRETS_KEK` (32 bytes), `AI_API_KEY`                                                                    | Every service, via envFrom. `AI_API_KEY` is also a `secretKeyRef`. It is the bearer token for `api-ai.watteel.com`.                                                                                                                                  |
| `novaforge-secrets`  | `REDIS_URL` = `redis://novaforge-redis:6379`                                                                                           | Every service                                                                                                                                                                                                                                        |
| `novaforge-secrets`  | `S3_ENDPOINT` = `<namespace>.compat.objectstorage.me-dubai-1.oraclecloud.com` (host only, no scheme), `S3_ACCESS_KEY`, `S3_SECRET_KEY` | Every service, for object storage: OCI Object Storage buckets `novaforge-ci` and `novaforge-releases`, through the customer secret key of `novaforge-objects` (`oci/novaforge.tf`). TLS and the region come from `objectStorage` in values-oke.yaml. |
| `novaforge-secrets`  | optional: `SSH_HOST_KEY` (PEM), `MCP_TOKEN`, `EMBED_API_KEY`, `RERANK_ENDPOINT`, `RERANK_MODEL`, `RERANK_API_KEY`                      | git-platform (an ephemeral key is generated without it, and SSH is not exposed), mcp-server, engineering-graph                                                                                                                                       |
| `novaforge-database` | `uri`: a PostgreSQL URL to `shared-pg-rw.shared-pg.svc:5432/novaforge` as role `novaforge`                                             | Every service's `DATABASE_URL`                                                                                                                                                                                                                       |
| `ocir-pull`          | `.dockerconfigjson` (type `kubernetes.io/dockerconfigjson`) for `dxb.ocir.io`                                                          | Every NovaForge pod's `imagePullSecrets`                                                                                                                                                                                                             |
| `novaforge-oidc`     | `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_REDIRECT_URI`: written by the zitadel-config runner, not SOPS                                   | edge (sign-in through Zitadel); identity reads the password-login flag only                                                                                                                                                                          |

Leave these out of `novaforge-secrets`: `DATABASE_URL` (it would be overridden anyway) and
`POSTGRES_PASSWORD` (there is no bundled PostgreSQL).

Do not create `novaforge-openbao`, `novaforge-deployment-config` or
`novaforge-semantic-producer` on OKE. Each one turns on a feature that "Isolation on OKE"
keeps off.

The OIDC sign-in work adds its own existing-Secret value (`oidc.existingSecret`) and keys.
That Secret is listed with that change, not here.
`internal/service`'s `TestOKEDocumentListsEverySecretTheChartNeeds` fails if a Secret or
key the OKE render needs is missing from this section.

## What has and has not been verified

Verified offline:

- `helm template novaforge deploy/helm/novaforge -n novaforge -f values-oke.yaml` renders 11
  Deployments, 11 Services, 3 PVCs and 1 HTTPRoute. There are no Secrets, RBAC objects,
  Cilium objects, Certificates or LoadBalancers.
- The render is identical across runs.
- kw's render is byte-identical to the one before this change.
- The `internal/service` chart tests cover the OKE render.
- The HTTPRoute was checked against the live CRD's schema without being applied (see the
  change's report). Its rule order and regex are tested in Go against sample paths.

Not verified:

- Anything on the cluster: no server-side dry-run (the namespace does not exist), no pull
  from OCIR, no CNPG connection, no route acceptance by Envoy Gateway, no git clone or push
  through the Gateway.
- Whether `api-ai.watteel.com` serves the kw model names, and whether `embed` returns 768
  dimensions.
