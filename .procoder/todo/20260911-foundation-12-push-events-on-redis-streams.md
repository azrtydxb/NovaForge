# foundation 12: Push events on Redis Streams

Status: open
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

- [ ] Write the failing test `internal/events/stream_test.go`: `func TestPublishAndConsume(t *testing.T)` publishes a `PushEvent`, reads it back with `XREADGROUP`, and asserts the round-tripped `NewSHA` matches; `func TestEnsureGroupIdempotent(t *testing.T)` calls `EnsureGroup` twice and asserts the second call returns nil rather than a BUSYGROUP error. Skip both when `TEST_REDIS_URL` is unset. Run `go test ./internal/events/` — expect FAIL with "undefined: events.Publish".
- [ ] Implement `Publish` marshalling to JSON into the field `data` via `XADD`, and `EnsureGroup` calling `XGroupCreateMkStream` and swallowing only errors whose text contains `BUSYGROUP`.
- [ ] Wire the receive-pack path from Task 11 to publish one `PushEvent` per updated ref after the git process exits zero.
- [ ] Run `TEST_REDIS_URL=redis://localhost:6379 go test ./internal/events/` — expect PASS.
- [ ] Commit as `feat: publish push events to redis streams`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
