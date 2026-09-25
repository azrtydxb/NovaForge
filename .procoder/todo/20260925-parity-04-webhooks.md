# Webhooks (S-24)

Status: open
Created: 2026-09-25

## Description

There is no webhook code at all, which is usually the first thing missed when
integrating anything external. The Redis streams already carry pushes, runs and CI
results, so this is a consumer and a delivery record rather than a new event system.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [ ] `internal/webhooks TestWebhookDelivered` passes: a push delivers a POST whose
      `X-NovaForge-Signature` is HMAC-SHA256 of the exact body under the hook's secret,
      and the delivery row records the response status.
- [ ] `TestWebhookRetriesAreBounded` passes: a hook whose endpoint always fails is retried
      to the bound and then left failed, not retried forever.
- [ ] `TestWebhookSecretNeverReadBack` passes: a secret can be set and rotated and no read
      returns either value.
- [ ] The worker is a Redis consumer group so two replicas do not deliver the same event
      twice, and a test asserts it is registered by `cmd/git-platform` — a worker nothing
      starts is this repository's most common defect.
- [ ] Routes under `/orgs/{org}/repos/{repo}/hooks` exist, `make openapi` leaves no diff,
      and the Repos screen shows hooks with delivery history.

## Evidence

<!-- Filled at close time. -->
