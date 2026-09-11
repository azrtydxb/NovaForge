# governance 08: Broker-down posture in job scheduling

Status: open
Created: 2026-09-11

## Description

Plan step 8 of `.procoder/plans/governance.md`, which exists to: Make the platform, not the agent, the authority on merge: a gate controller that runs after an agent declares completion and cannot be bypassed or self-approved, a policy-driven approval model the model can never alter, and a secret broker issuing short-lived scoped credentials instead of durable secrets.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/ci/credentials.go`, `internal/ci/credentials_test.go`

Interfaces: produces `ci.ResolveJobCredentials(ctx context.Context, b secrets.BrokerClient, job Job, g capability.Grant) (map[string]string, error)` returning `ci.ErrCredentialsUnavailable` when the broker cannot be reached and the job declares at least one secret, and an empty map with a nil error when the job declares none.

## Acceptance criteria

- [ ] Write the failing test `internal/ci/credentials_test.go`: `func TestCredentialFreeJobRunsWhenBrokerDown(t *testing.T)` stubs a broker returning `codes.Unavailable`, passes a job declaring no secrets, and asserts a nil error and an empty map — the job proceeds; `func TestCredentialJobBlocksWhenBrokerDown(t *testing.T)` passes a job declaring `DEPLOY_KEY` and asserts an error satisfying `errors.Is(err, ci.ErrCredentialsUnavailable)`; `func TestJobStaysQueuedNotFailed(t *testing.T)` asserts the scheduler leaves such a job in `pending` rather than marking it `failure`, so it runs when the broker returns. Run `go test ./internal/ci/` — expect FAIL with "undefined: ci.ResolveJobCredentials".
- [ ] Implement the split so the broker is contacted only when `job.Secrets` is non-empty, and wire the scheduler to skip claiming a blocked job rather than failing it.
- [ ] Run `go test ./internal/ci/` — expect PASS.
- [ ] Commit as `feat: fail closed on secret broker outage without failing safe jobs`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
