# Gap closure G11: operational proposals

Status: closed 2026-09-27
Created: 2026-09-27

## Description

Prepare shared-infrastructure decisions with exact scope and current evidence.
Do not turn NovaForge release authority into broad host maintenance authority.

## Acceptance criteria

- [x] Nexus trim proposal identifies exact PVC, privileges, schedule, verification and rollback; manifest passes server dry-run without being applied.
- [x] Capacity observations distinguish measured growth from an unsupported forecast.
- [x] Accepted development OpenBao custody remains unchanged; production custody has a separate readiness path.
- [x] Current agent/model acceptance is checked before reopening the historical gateway incident.

## Evidence

The concrete Nexus PVC trim proposal and suspended manifest identify the exact namespace/PVC/PV, least required privileges, weekly window, bounded execution, verification and rollback. Server dry-run passed; the shared-infrastructure change was not applied. Two same-day capacity observations are recorded as measured growth, not a capacity forecast.

Accepted development OpenBao root/unseal custody is unchanged. Production custody has a separate readiness path. Final cluster acceptance on application 19b29cc passed both agent and agent_ci, so the historical model-gateway incident did not recur and no external project was modified or issue reopened.

The secrets suite exposed expiry of NovaForge's dedicated development CI issuer (September 26). The bounded hack/renew-dev-ci-issuer.py verifies its exact sole role/domain before renewing only that expired fixture CA; its 600-second default/3600-second maximum leaf policy and unseal custody were preserved. Subsequent issuance acceptance is part of G12. The failed request's unknown-outcome record remains intact, with an explicit provider reconciliation follow-up rather than a fabricated cleanup verdict.
