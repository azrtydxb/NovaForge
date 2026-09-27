# Audited reconciliation for unknown provider issuance

Status: open
Created: 2026-09-27

## Description

The September 27 full cluster run found an expired development PKI CA. The broker
correctly failed closed on HTTP 400 and retained an unknown issuance obligation
without a provider lease handle. Renewing the CA fixes subsequent issuance but
cannot honestly mark the historical outcome resolved. Add a narrow operator
resolution path after designing the evidence contract; do not guess a handle,
retry issuance, or delete the row.

## Acceptance criteria

- [ ] Define provider-specific evidence that proves no issuance or that all possibly issued target credentials are revoked/expired, independent of the broker's local token lifetime.
- [ ] Bind an authenticated operator receipt to the exact organization, attempt, provider identity and immutable evidence; retain the unknown history and resolution actor/time.
- [ ] Refuse mismatched evidence, concurrent/late issuance and cross-organization resolution; never permit a repository caller to self-attest cleanup.
- [ ] Exercise actual provider failure and operator reconciliation through API/GUI, preserving the owning CI cleanup obligation until resolution succeeds.

## Evidence

- Failed fixture CI job: `31e98bd6-5f24-4bdf-aa1c-61435baadaed`.
- Unknown lease reservation: `c20b6e47-1ac1-4236-b62c-e14621264566` in the existing `nfsecrets` organization.
- Provider returned HTTP 400; no credential reached the job and no lease handle was returned. The configured issuer had expired September 26 at 13:14:28 UTC.
- The issuer was renewed September 27 through the bounded development fixture tool. The reservation remains visible as unknown; no database state was falsified.

### Follow-up provider inspection

On September 27 the actual provider's `bao audit list -format=json` returned no
enabled audit devices. The mounted `NF_CI_CERT` binding exports `private_key`
from `pki/issue/novaforge-ci`, not a certificate-only target credential. Thus a
CA expiry or local ten-minute lifetime cannot establish that an arbitrary private
key is unusable at every target. No historical request ID or provider lease handle
is recorded. This is an evidence blocker for clearing this particular reservation,
not grounds to silently mark it revoked. Future operator reconciliation must bind
provider evidence and authenticated operator identity; a signed human assertion
without verifiable target/provider evidence is insufficient.
