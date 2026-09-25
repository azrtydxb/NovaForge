# Webhooks (S-24)

Status: closed 2026-09-25
Created: 2026-09-25

## Description

There is no webhook code at all, which is usually the first thing missed when
integrating anything external. The Redis streams already carry pushes, runs and CI
results, so this is a consumer and a delivery record rather than a new event system.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [x] `internal/webhooks TestWebhookDelivered` passes: a push delivers a POST whose
      `X-NovaForge-Signature` is HMAC-SHA256 of the exact body under the hook's secret,
      and the delivery row records the response status.
- [x] `TestWebhookRetriesAreBounded` passes: a hook whose endpoint always fails is retried
      to the bound and then left failed, not retried forever.
- [x] `TestWebhookSecretNeverReadBack` passes: a secret can be set and rotated and no read
      returns either value.
- [x] The worker is a Redis consumer group so two replicas do not deliver the same event
      twice, and a test asserts it is registered by `cmd/git-platform` — a worker nothing
      starts is this repository's most common defect.
- [x] Routes under `/orgs/{org}/repos/{repo}/hooks` exist, `make openapi` leaves no diff,
      and the Repos screen shows hooks with delivery history.

## Evidence

Verified by me, not taken from the agent's report:

- `go test ./internal/webhooks/ ./internal/gitops/` on the merged tree — both ok.
- Red-green on the seam that matters: replacing the worker's start with `_ = worker`
  makes `TestGitPlatformStartsTheDeliveryWorker` fail with "cmd/git-platform never calls
  (*webhooks.Worker).Run: no push would ever reach a registered hook". Restored and
  green. That is this repository's signature defect guarded by a test.
- `make openapi` leaves no diff; `gofmt -l internal cmd` prints nothing; `go build ./...`
  and `go vet` clean after resolving five merge conflicts with the releases lane.

Reported by the agent, and consistent with what I saw:

- Mutation checks it ran and reverted: signing `body+0x27` instead of the body, ignoring
  `MaxAttempts`, and storing the secret unencrypted each made the corresponding test
  fail. So the green tests bite.
- It found a real hole mid-build: `ListDeliveries` returned another organization's
  delivery history for a foreign hook id. It now resolves the hook in scope first and
  answers "no such hook".
- A hook's `Hook` type carries no secret field at all, rather than one callers must
  remember not to render. An empty KEK refuses to store a secret rather than deriving a
  key from the empty string.
- An agent cannot register a webhook: the managing path refuses non-user actors, because
  egress is not covered by any capability grant.

Met as this task is written, and NOT the whole of S-24:

- Only pushes are delivered. S-24 also names Engineering Runs and CI results, and
  neither is published on any stream today — the stream carrying run deletions has run
  ids with no repository, so there is nothing routable to a repository's hooks. The spec
  criterion is recorded as **partial** in traceability, not covered, and closing it needs
  a publisher in `internal/ci` and `work-reviews`.
- Nothing ran on the cluster. git-platform is under `networkPolicy.airGapped`, so a hook
  reaches an in-cluster endpoint and a public-internet endpoint is dropped by Cilium.
  Both facts are now recorded in BUILD-STATUS.
- No `nf` CLI subcommand and no e2e suite for hooks.
