# Gap closure G09: governed deployment production integration

Status: open
Created: 2026-09-27

## Description

Wire the existing governed-deployment service and qualify a real provider-backed, target-expiring credential path. Build an immutable runner/chart fixture and exercise it through REST and GUI on kw.

## Acceptance criteria

- [ ] Gates starts the deployment service, migration, cleanup and success outbox; edge/GUI expose the supported lifecycle.
- [ ] Qualified Kubernetes credentials are minted by OpenBao, validated against the target and bounded by signed token expiry; unqualified providers remain refused.
- [ ] Approved fixed chart/image creates an observed workload at the configured target.
- [ ] Independent approval, denial, expiry, changed intent, failure/retry and duplicate execution protections are proven.
- [ ] Credentials are revoked/fenced and deployment evidence reaches the graph.

## Evidence

- Source audit found no production NewConfiguredService caller and PrepareDeployment unconditionally refused hard-expiry qualification. These are implementation work, not just missing operator configuration.
- OpenBao's Kubernetes secrets engine documentation confirms generated service account tokens, per-request TTL and automatic service-account/role-binding cleanup. Qualification additionally verifies the signed token against the actual target.
