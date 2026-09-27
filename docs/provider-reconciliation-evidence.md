# Unknown provider issuance: evidence required for reconciliation

An unknown issuance reservation remains a cleanup obligation. Retrying issuance,
renewing an issuer, waiting out a broker token, or deleting the reservation does
not establish what the provider did. A repository owner cannot attest away this
obligation. This document defines the admission contract for a future operator
resolution endpoint; that endpoint is not implemented or enabled.

## Admissible evidence

For OpenBao, a no-issuance resolution requires a completed provider response audit
record, correlated to the exact issuance request, whose provider-specific outcome
proves that no credential was issued. An HTTP status recorded only by the broker
is insufficient after an ambiguous request. The verifier must authenticate the
audit source and check its integrity and completeness. A request-only record,
missing response, audit-device configuration, or an operator signature without
the underlying provider evidence is insufficient.

Alternatively, evidence must enumerate every possibly issued credential and
independently verify revocation or expiry at every target where it could grant
authority. For a certificate-only binding, issuer identity, certificate serial,
validity interval, target trust policy and the target's enforcement behavior are
part of that proof. Certificate expiry does not revoke an exported private key.
An unconstrained private-key export has no general expiry-based reconciliation.

## Receipt and concurrency contract

A receipt must bind the organization, reservation, issuance attempt, provider
endpoint and mount, issuer identity, exact request correlation identifier,
credential projection, evidence digest and immutable evidence location. Retain
the original unknown outcome, resolution type, authenticated operator identity,
verification time and verifier version. Organization membership alone must not
grant reconciliation authority; the operator trust configuration belongs outside
repository control.

The verifier must reject foreign organizations, substituted providers or
attempts, mutable or missing evidence, incomplete credential inventories and
unsupported projections. Verification may occur outside a database transaction,
but committing its result must recheck the exact issuance generation under the
existing organization/run/attempt fences. A live or late issuance request cannot
be resolved concurrently. A stale receipt cannot clear a newer attempt.

Only the secrets owner may resolve its obligation. CI must continue observing
pending cleanup through the broker API until resolution commits; no reconciliation
operation may update another service's schema. API and GUI must present the
evidence-backed resolution separately from historical issuance state.

## September 27 historical reservation

Reservation `c20b6e47-1ac1-4236-b62c-e14621264566` has no provider lease handle or
request correlation ID, and the inspected OpenBao deployment has no audit
devices. `NF_CI_CERT` exported `private_key` from `pki/issue/novaforge-ci`.
The expired issuer's HTTP 400 and its subsequent renewal do not satisfy either
proof above. Independent historical provider or target evidence is required.
The reservation and owning CI cleanup obligation remain unresolved.

Before implementation, select an actual evidence source and its authentication
and retention mechanism, then exercise a new correlated provider failure against
that source. A generic “mark resolved” endpoint would bypass the contract and is
deliberately not a substitute.
