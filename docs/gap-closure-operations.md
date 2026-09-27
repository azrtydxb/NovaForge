# Git-host and governed-deployment qualification

All builds use kw BuildKit and immutable commit tags. Apply application changes
with `hack/deploy.sh`; a direct deployment image patch bypasses Helm ownership.

## Connected Git hosts

The default outbound allowlist is empty. Configure `outbound.destinations` with
an exact scheme, host, port and allowed address ranges. Application checks and
Cilium rules travel together. DNS answers are validated and pinned for each
connection; proxies and redirects cannot widen the destination. Private hosts
need an explicit private CIDR. Loopback, link-local and metadata addresses remain
refused. Public `0.0.0.0/0` does not grant access to private addresses.

`hack/prepare-git-host-fixture.sh /tmp/connected-values.json` creates a disposable
upstream/receiver and writes reviewable overrides for its exact address plus
github.com:443. Review/apply those overrides through Helm, retaining the existing
installation values. Remove the fixture rule when deleting its namespace. To
return to the default disconnected posture, set `outbound.destinations: []` and
apply through Helm. Do not grant unrestricted egress to make an import work.

Git import preserves refs and history. It does not migrate LFS payloads, release
assets, accounts, issues or CI records. Tokens belong in `nf repo import
--credential-file`, stdin, or the GUI token field, never URLs or command-line
arguments. Stopping a mirror keeps its fetched history and permits local pushes.

SSH LFS uses the ordinary git-lfs client: the SSH key authorizes a short-lived,
repository/operation-scoped ticket, and payload bytes travel over verified HTTPS.
Set `lfs.publicURL` to an address the client can reach and include that address in
the certificate. Clients must trust the installation CA. Session tickets do not
authorize ordinary Git, another repository or the opposite transfer operation.

## Blob reclamation

The Git service runs a durable exact-key cleanup queue. Metadata deletion commits
before object deletion; failures retain retry intent. Fork references keep an
immutable payload alive until its last reference disappears. An upload commits
cleanup intent before external I/O and locks it while publishing metadata.

`cmd/blob-audit` is a bounded, organization-scoped reconciliation tool. Its default
is report-only; use a grace period of at least 24 hours. Review candidates before
requesting enqueue. It queues existing unreferenced LFS/release keys for the same
collector and does not directly delete historical objects or sweep other stores.
Keep queue attempts/errors visible during outages; an empty metadata table alone
does not prove storage reclamation.

## Governed deployment fixture

1. Build committed source with `hack/build-images.sh`. Build the approved chart
   and runner with `hack/build-deployment-fixture.sh`; retain its image digest and
   chart checksum. The runner image contains the approved chart and Helm binary.
2. Run `hack/prepare-deployment-fixture.py RUNNER_IMAGE@sha256:... ARTIFACT_DIGEST
OUTPUT_DIRECTORY`. It prepares dedicated execution/target namespaces, exact
   roles, a Kubernetes secrets-engine binding and independently authenticated
   author/reviewer accounts. Secret values stay in Kubernetes Secrets.
3. Apply `deployment-values.json` through `hack/deploy.sh` together with the
   existing installation and connected-host values.
4. Run `hack/e2e-in-cluster.sh governed_deploy` and
   `python3 hack/qualify-deployment-expiry.py`. The latter waits for a real
   ten-minute credential to expire and verifies target authentication refusal.
5. Keep actual operation/attempt/cleanup and graph evidence. A created workload
   alone is insufficient: ambiguous evidence remains uncertain and requires
   reconciliation. Do not retry an uncertain delivery as a new operation.

The target is fixed by operator configuration to organization, repository,
environment, release, namespace, chart and immutable executor image. The author
requests an immutable artifact, an independent authorized reviewer approves it,
and the service owns execution and credential cleanup. Target Kubernetes tokens
are verified against the target, have a signed expiry bounded by the attempt and
any narrower run/grant authority, and are backed by revocable generated service
accounts. Unqualified provider bindings remain refused.

The qualification token combining the existing broker policy with the disposable
deployment policy lasts 24 hours. After qualification, restore the original
OpenBao operator Secret reference and remove the deployment configuration through
Helm before revoking that temporary token. Revoke outstanding fixture leases,
remove its provider mount/policy and dedicated namespaces/Secrets, and delete the
fixture organization. Do not leave existing CI dependent on an expiring test token.
Real staging/production target onboarding remains separate operator configuration.

The default acceptance harness now includes the three additional Git-host,
rotation and governed-deployment suites. Prepare their operator fixtures before
running all suites. Certificate rotation creates/removes its own Certificate and
transport pod without changing the shared issuer.

## Shared infrastructure

The Nexus recurring-trim proposal is in
`.procoder/proposals/nexus-trim-20260927.md` and its suspended manifest. It was
server-dry-run validated, not applied. Accepted OpenBao development custody remains
unchanged; production unseal custody is an explicit separate readiness decision.
