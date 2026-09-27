# Move NovaForge kw deployment to Kuvryn Sync

Status: open
Created: 2026-09-27

## Description

User authorized moving NovaForge to the same Kuvryn Sync deployment mechanism
used by other kw applications. Preserve the existing qualified application images,
credentials, database contents, volumes, endpoints and operator configurations.

## Acceptance criteria

- [ ] Git contains sanitized kw values and Repository/Application definitions.
- [ ] Existing Secrets and runtime cluster RBAC remain operator-owned; Sync is namespace-scoped and PVCs cannot be pruned.
- [ ] Helm ownership is handed over only for reviewed rendered objects; the old deployment command refuses competing writes.
- [ ] Live Application is Synced/Healthy, data/volume identities are preserved, and targeted cluster suites pass.
- [ ] Image promotion, rollback, bootstrap boundaries and migration evidence are documented and published.

## Evidence

Pending migration verification.
