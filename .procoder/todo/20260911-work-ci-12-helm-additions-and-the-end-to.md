# work-ci 12: Helm additions and the end-to-end work-to-artifact test

Status: closed 2026-09-12
Created: 2026-09-11

## Description

Plan step 12 of `.procoder/plans/work-ci.md`, which exists to: Deliver Work Items, Engineering Runs, and the CI system: typed engineering intent that can be assigned to a human or an agent, pull requests that carry plan and proof rather than only a diff, and runners that receive pushed jobs, stream logs, and upload artifacts.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `deploy/helm/novaforge/templates/work-reviews.yaml`, `deploy/helm/novaforge/templates/ci-runner.yaml`, `deploy/helm/novaforge/templates/runner.yaml`, `deploy/helm/novaforge/values.yaml`, `tests/e2e/work_ci_test.sh`

Interfaces: produces Services `novaforge-work-reviews:9093` and `novaforge-ci-runner:9094`, and a `runner` Deployment with a configurable replica count and label set from `values.yaml` key `runner.labels`.

## Acceptance criteria

- [x] Write the failing test `tests/e2e/work_ci_test.sh`: on the kind cluster from the foundation plan, `helm upgrade novaforge ./deploy/helm/novaforge --wait --timeout 10m`, then push a repository containing `.novaforge/workflow.yaml` with a single job running `echo hello && echo hi > out.txt`, poll `nf ci runs` until the run reports `success` within 300s, assert the job log contains `hello`, and assert the artifact `out.txt` is downloadable. Run `bash tests/e2e/work_ci_test.sh` — expect FAIL with "Error: no matching deployment novaforge-ci-runner".
- [x] Write the three templates, giving the runner Deployment no inbound Service at all, since runners dial out and never accept connections.
- [x] Extend `values.yaml` with `runner.replicas` defaulting to `2`, `runner.labels` defaulting to `["linux"]`, and MinIO credentials wired into the ci-runner Deployment.
- [x] Run `bash tests/e2e/work_ci_test.sh` — expect PASS.
- [x] Commit as `feat: deploy work-reviews, ci-runner, and runners via helm`.

## Evidence

- Task 12: the Helm additions for work-reviews, ci-runner and the CI runners, plus the end-to-end work-to-artifact test.
- Green on the LIVE kw cluster: `bash tests/e2e/work_ci_test.sh` returns
  "PASS: Work Items and CI work end to end on the kw cluster."
  Its six steps are: an account, organization and repository created through the REST API; a Work Item created and listed; a runner registered into that organization; a push of a .novaforge/workflow.yaml scheduling a run; the run reaching success; and the job's log and its declared artifact both retrievable.
- Getting there found eight real defects that unit tests could not have caught, because each component was internally consistent and only the seams between them were wrong. In order: the CI scheduler had no caller identity so git-platform refused its workflow fetch; push events carried a nil repository id so nothing was ever scheduled; there was no CI query API at all, so a run could not be observed; the runner's ServiceAccount was gated on the same value as its Deployment, so the identity was missing exactly when something used it; ClaimJob and Dispatch were both written and neither was ever called; the default job image had no git, so the checkout exited 127; the clone URL addressed the repository by id while the route is keyed by name, failing after authentication had already succeeded; and nothing rolled job statuses up, so a run stayed "running" after its last job went green.
- One of those was a security defect, not a functional one: the job claim had no organization predicate, so a runner registered to one organization could be handed another organization's job, its source and its credentials. It surfaced as two tests interfering. TestClaimNeverCrossesOrganizations now pins both directions.
- A design error was corrected by the cluster rather than by review: artifacts were first collected by exec-ing into the pod afterwards, which cannot work because a finished job's container has terminated. The job now captures its own declared outputs before exiting, which also removed the pods/exec permission the runner had needed.
- `go build ./...`, `go vet ./...` and `go test -count=1 ./...` are clean across 32 packages against the real PostgreSQL, Redis and MinIO.
