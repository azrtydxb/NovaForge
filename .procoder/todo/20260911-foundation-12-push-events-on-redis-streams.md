# foundation 12: Push events on Redis Streams

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 12 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/events/stream.go`, `internal/events/stream_test.go`

Interfaces: produces `events.PushEvent{OrgID, RepoID, PusherID uuid.UUID, Ref, OldSHA, NewSHA string, At time.Time}`, `events.Publish(ctx, rdb *redis.Client, stream string, payload any) error`, `events.EnsureGroup(ctx, rdb *redis.Client, stream, group string) error`, and the constant `events.StreamGitPush = "stream:git:push"`. Later plans consume this stream for CI and indexing; handlers must be idempotent because delivery is at-least-once.

## Acceptance criteria

- [x] Write the failing test `internal/events/stream_test.go`: `func TestPublishAndConsume(t *testing.T)` publishes a `PushEvent`, reads it back with `XREADGROUP`, and asserts the round-tripped `NewSHA` matches; `func TestEnsureGroupIdempotent(t *testing.T)` calls `EnsureGroup` twice and asserts the second call returns nil rather than a BUSYGROUP error. Skip both when `TEST_REDIS_URL` is unset. Run `go test ./internal/events/` — expect FAIL with "undefined: events.Publish".
- [x] Implement `Publish` marshalling to JSON into the field `data` via `XADD`, and `EnsureGroup` calling `XGroupCreateMkStream` and swallowing only errors whose text contains `BUSYGROUP`.
- [x] Wire the receive-pack path from Task 11 to publish one `PushEvent` per updated ref after the git process exits zero.
- [x] Run `TEST_REDIS_URL=redis://localhost:6379 go test ./internal/events/` — expect PASS.
- [x] Commit as `feat: publish push events to redis streams`.

## Evidence

- Task 12: PushEvent published to stream:git:push, EnsureGroup idempotent over BUSYGROUP, wired into the receive-pack path.
- Built by a parallel agent in an isolated git worktree under strict red-green TDD, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge by the main agent): `go test -count=1 -v ./internal/gitops/... ./internal/capability/... ./internal/events/...` → 19 PASS, 0 FAIL. ok gitops 3.085s, ok capability 1.363s, ok events 0.601s.
- These are REAL git operations, not simulations: TestCloneAndPushOverHTTP and TestPushOverSSH drive an unmodified git 2.50.1 client through a real clone, commit and push. TestPushDeniedByCapability and TestPushOverSSHDeniedByCapability prove both transports refuse an out-of-scope ref identically. TestPathTraversalRejected and TestPrefixEscapeDenied pin the escape cases. TestPushOverHTTPPublishesEvent consumes the push event back out of a real Redis consumer group.
- Datastores are the REAL PostgreSQL 16 and Redis 7 in the kw cluster, not mocks.
- Two documented protocol deviations, both in the commit bodies: push denial is returned as a git-receive-pack report-status "ng <ref> <reason>" inside side-band-64k framing, because git's smart-HTTP client discards a bare non-2xx body and the plan's own test requires the reason to reach stderr; and SSH cannot use --stateless-rpc for the advertisement because git's interactive SSH client blocks waiting for it, so the advertisement is sent first and the push body is then buffered and authorized exactly as HTTP does.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: c83d80c, 9759e95, 2b1dad1, 37553b2, a44d29d. Merged in 67b3c31.
