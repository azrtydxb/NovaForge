# Move NovaForge kw deployment to Kuvryn Sync

Status: closed 2026-09-27
Created: 2026-09-27

## Description

User authorized moving NovaForge to the same Kuvryn Sync deployment mechanism
used by other kw applications. Preserve the existing qualified application images,
credentials, database contents, volumes, endpoints and operator configurations.

## Acceptance criteria

- [x] Git contains sanitized kw values and Repository/Application definitions.
- [x] Existing Secrets and runtime cluster RBAC remain operator-owned; Sync is namespace-scoped and PVCs cannot be pruned.
- [x] Helm ownership is handed over only for reviewed rendered objects; the old deployment command refuses competing writes.
- [x] Live Application is Synced/Healthy, data/volume identities are preserved, and targeted cluster suites pass.
- [x] Image promotion, rollback, bootstrap boundaries and migration evidence are documented and published.

## Evidence

Migration and verification are recorded in `.procoder/evidence/kuvryn-sync-20260927.md`
and its companion public receipts. Kuvryn Sync v0.7.1 now follows main and renders
sanitized values-kw.yaml; the actual Application is Synced/Healthy with 35 healthy
managed resources and all 12 deployments ready. Application binaries remain the
qualified c8ade67 release.

- Secret UID and all original credential bytes were preserved; only existing
  datastore-value aliases were added. No Secret is rendered in kw mode.
- Runtime cluster RBAC remains operator-owned. Actual deployer authorization
  checks refuse Secret access, PVC deletion, foreign-namespace changes and
  cluster RBAC creation. All three PVC UIDs, PV bindings and capacities match.
- PostgreSQL backup restored successfully with 94 tables; private backup and
  historical Helm records were retained outside Git. Ten release-record Secrets
  were retired without uninstalling resources. Only 35 reviewed objects changed
  field ownership. The legacy deploy command refuses competing Helm writes.
- All nine service images, runner and analysis sandbox passed both registry
  connector checks; unpublished-image promotion refused without editing values.
- Eight targeted cluster suites have passing evidence: six initially, followed
  by successful deploy and agent reruns. The initial ownership-probe prerequisite
  failure and agent failure remain documented. Actual agent-CI also succeeded.
- Chart regression was observed red then green; service/chart tests, shell syntax,
  Helm lint, server-side validation and repository gates pass. Conflict-policy
  refusal was observed and its probe was restored without a pod-template change.

Workflow, bootstrap boundaries, image promotion and rollback are published in
`deploy/kuvryn-sync/README.md`. Builds remain explicit operator steps; no automatic
image-building CI is claimed. The separate historical provider-evidence blocker
is unchanged.
