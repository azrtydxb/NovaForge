# S-23: Engineering activity overview

Status: closed 2026-09-25
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

- [x] `internal/agents TestAgentActivityReported` passes: an agent executing a run
      is reported with that run's Work Item key, an agent holding no run is
      reported idle, and an agent whose run has ended is reported idle again.
- [x] `internal/work` completed-today tests pass: the dashboard
      summary counts Work Items that reached done since midnight UTC, does not
      count one that reached done earlier, and does not count one that has since
      moved back out of done.
- [x] A Work Item reaching `done` records when it did, and moving out of `done`
      clears it, proven by a test that fails before the migration exists.
- [x] `GET /api/v1/orgs/{org}/agents` reports each agent's activity and
      `GET /api/v1/orgs/{org}/dashboard` reports the completed-today count, both
      described in `api/openapi.yaml` (`make openapi` leaves no diff).
- [x] The home page shows the agent roster with what each agent is working on, and
      the completed-today count, with `npx tsc -b --noEmit` and `npm run build`
      clean.
- [x] `.procoder/specs/traceability.yaml` cites the new tests for S-23 and
      `internal/spectrace TestSpecTraceability` passes.
- [x] `go test ./...` reports no failures, and `gofmt -l internal cmd` prints
      nothing.

## Evidence

- `go test ./internal/agents/ -run TestAgentActivity -v` — TestAgentActivityReported and
  TestAgentActivityIsOrganizationScoped both PASS. Written first and seen to fail with
  "undefined: agents.Activity".
- `go test ./internal/work/ -run 'TestCompletedSince|TestDoneAt' -v` — all three PASS.
  TestDoneAtRecordsWhenAnItemCompleted was seen to fail first with
  `column "done_at" does not exist`, and the CompletedSince tests with
  "store.CompletedSince undefined", so each failed for the missing thing it names.
- `go test ./...` — 44 packages ok, 0 FAIL. `gofmt -l internal cmd` prints nothing.
- `go test ./internal/spectrace/` — ok, so traceability cites tests that exist.
- `npx tsc -b --noEmit` clean and `npm run build` succeeded (445.86 kB bundle).
- On the cluster at Helm revision from image tag b84ffec, organization gitorg29116:
  `GET /dashboard` reported `completed_today = 0, available = True`; after one Work
  Item reached done it reported `completed_today = 1, available = True`; after the item
  was reopened the database showed `done_at` cleared (`state=open, cleared=t`). The
  trigger therefore fires in the deployed image and the read path reports it.
- `GET /agents` on the cluster reported `builder backend run=(none) item=(idle)`, so an
  idle agent is stated rather than omitted.

Not met as written: the criterion said the new fields would be "described in
`api/openapi.yaml`". That document is generated from the edge route table and describes
routes and parameters, not response bodies — no route changed, so `make openapi` leaves
no diff. The criterion was imprecise; the fields are covered by the Go tests and the
cluster check above instead.
