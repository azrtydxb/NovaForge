# S-23: Engineering activity overview

Status: open
Created: 2026-09-25

## Description

Section 24 of `NovaForge_AI_Native_Git_Platform.md` asks the product to lead with
engineering activity rather than repository browsing: a roster showing what each
agent is doing right now, and a count of work just completed. That intent was
never carried into the spec or the six plans, so it is the one part of the design
document with visible unbuilt scope. Everything else in the six MVP phases is
built, covered and deployed.

Two things are missing. The platform cannot say what an agent is doing — `Agent`
carries id, name, role, model and enabled, and nothing joins an agent to the run
it is executing, so the Agents screen shows a list of names and the home page
shows only counts. And nothing records when a Work Item reached `done`:
`work.work_items` has `created_at` and a state, so "completed today" cannot be
derived at all and needs a column that is written when the state becomes done.

Done looks like: the platform reports each agent's current Work Item or that it
is idle, and how many items reached done since midnight UTC; both numbers are the
platform's own, computed once, server-side; and the home page shows the roster
and the count instead of the client deriving either.

## Acceptance criteria

- [ ] `internal/agents TestAgentActivityReported` passes: an agent executing a run
      is reported with that run's Work Item key, an agent holding no run is
      reported idle, and an agent whose run has ended is reported idle again.
- [ ] `internal/reviews TestWorkCompletedTodayCounted` passes: the dashboard
      summary counts Work Items that reached done since midnight UTC, does not
      count one that reached done earlier, and does not count one that has since
      moved back out of done.
- [ ] A Work Item reaching `done` records when it did, and moving out of `done`
      clears it, proven by a test that fails before the migration exists.
- [ ] `GET /api/v1/orgs/{org}/agents` reports each agent's activity and
      `GET /api/v1/orgs/{org}/dashboard` reports the completed-today count, both
      described in `api/openapi.yaml` (`make openapi` leaves no diff).
- [ ] The home page shows the agent roster with what each agent is working on, and
      the completed-today count, with `npx tsc -b --noEmit` and `npm run build`
      clean.
- [ ] `.procoder/specs/traceability.yaml` cites the new tests for S-23 and
      `internal/spectrace TestSpecTraceability` passes.
- [ ] `go test ./...` reports no failures, and `gofmt -l internal cmd` prints
      nothing.

## Evidence

<!-- Filled at close time. -->
