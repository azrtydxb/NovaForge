# NovaForge kw ownership migration to Kuvryn Sync

User authorized adopting the deployment mechanism used by the other kw apps.
The installed controller is Kuvryn Sync v0.7.1. Scout, Labbook and Novamem were
already managed by it. NovaForge now has a public Git Repository and Helm-rendered
Application in namespace `novaforge`, following `main` with `values-kw.yaml`.

## Preservation and ownership

The migration kept application image `c8ade67`, the qualified analysis image,
semantic producer configuration, original operator Secrets, endpoints and runtime
RBAC. Existing credential bytes and the service Secret UID were compared before
and after. Three aliases copied the existing datastore credentials into that
Secret; no replacement password was generated. No Secret is rendered in kw mode.

All three PVC UIDs, PV bindings and capacities are unchanged. PostgreSQL's custom
archive restored successfully into a disposable database with 94 application
tables; the restore database was then removed. The backup and Helm release/Secret
snapshots remain in the access-restricted local migration directory. Only the
archive digest and verification result are committed.

The server-side reviewed diff touched PostgreSQL/MinIO credential references and
zero-surge rollout settings, plus PVC prune protection. The initial server dry
run refused retained literal credential fields and an incompatible Recreate
strategy; the final render explicitly clears the former and uses RollingUpdate
with maxSurge 0/maxUnavailable 1. Both datastore rollouts completed. Their brief
single-instance restart temporarily affected dependent service readiness.

Only the 35 reviewed rendered resources transferred field ownership. Existing
operator/runtime RBAC and Secrets were excluded. 10 Helm release-record
Secrets were backed up and retired without uninstalling any workload. The Sync
Application uses namespace-scoped impersonation, automatic sync, conflictPolicy
fail, pruning, self-healing, bounded health/rollback policy and Orphan deletion.
PVCs also opt out of pruning and the deployer lacks their delete permission.
Actual authorization checks refused Secret reads/writes, PVC deletion,
cross-namespace Deployment changes and cluster RBAC creation. These are direct
API checks, not a claim that workload-edit permission isolates an untrusted Git
author from existing runtime service accounts or mounted Secrets.

## Release workflow and tests

`hack/promote-kw.sh` verified all nine service images, runner and analysis sandbox
through both Nexus push and pull endpoints with matching digests. It refused an
unpublished commit image without changing the desired values. The direct Helm
deployment command now refuses while Sync owns the namespace. Builds remain an
explicit operator step; no image-building CI was invented by this migration.

The chart's external-secret/RBAC/PVC regression failed before implementation and
passed after it. The whole service/chart package passes against the configured
environment. Shell syntax, Helm lint and server-side validation pass. The deployed
acceptance script was updated after its old `helm get values` path correctly
failed against the retired release; it now reads GitOps values and requires a
Healthy Application.

A metadata-only ownership probe acquired a separate Update field owner. Sync
refused takeover as configured, producing a Failed revision; it had no prior
healthy revision available to roll back to during this initial adoption. The
operator restored the label and relinquished only the probe's Update ownership.
No pod template changed. The subsequent Git revision reconciled Healthy and
provides a healthy rollback baseline. This is evidence of conflict refusal, not
an automatic self-heal success. The concurrent deploy suite failed its Healthy
prerequisite during that probe; its rerun is recorded separately.

Maintained workflow and operator boundaries: `deploy/kuvryn-sync/README.md`.
Raw public receipts: `.procoder/evidence/kuvryn-sync-20260927/`. Private credentials,
Helm value snapshots, old environment diffs and database contents are excluded.

## Final acceptance

All eight targeted cluster suites have passing evidence: airgap, gui, deploy,
work_ci, graph, agent, agent_ci and crossorg. The initial batch passed six and
failed two. Deploy was correctly blocked during the deliberate ownership-conflict
probe. The first agent run failed; its original harness retained only the state
before purging its disposable organization, so its root cause is not established.
The harness now reports its observable end reason on future failures. The rerun
passed both deploy (real HTTPS/SSH Git and cleanup) and agent (actual successful
run and committed branch). Agent-CI also completed successfully in the initial
batch. This is not a claim that the initial eight-suite batch was all green.

The final observed Application is Synced/Healthy, with all 35 managed resources
healthy and all 12 deployments ready. The source advanced through published Git
revisions automatically. A separate Healthy revision exists after the resolved
conflict, while the failed probe revision remains recorded. Chart regression,
Helm lint, shell syntax, server dry-run and repository checks pass (zero blocking
findings). The controller, other applications and runtime permissions were not
upgraded or broadened by this migration.
