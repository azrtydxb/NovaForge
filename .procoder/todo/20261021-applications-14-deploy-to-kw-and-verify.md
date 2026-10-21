# applications 14: Deploy to kw and verify

Status: open
Created: 2026-10-21

## Description

Task 14 of `.procoder/plans/applications.md` (spec `.procoder/specs/applications.md`): Deploy to kw and verify. Done when the task's steps are carried out exactly as the plan writes them, its tests were seen red then green, and nothing outside the task's Files list changed except what its steps name.

## Acceptance criteria

- [ ] `source hack/env.sh && go test ./... -count=1` shows no FAIL, `gofmt -l internal cmd` is empty, the web app builds, and `make openapi` leaves `api/openapi.yaml` unchanged.
- [ ] Images built with `hack/build-images.sh` and promoted with `hack/promote-kw.sh`; the `values-kw.yaml` diff is only `image.tag`; Sync reports novaforge Healthy/Synced at that commit, every pod is Running, and the new migrations are applied.
- [ ] `./hack/e2e-in-cluster.sh applications gui cli merge work_ci` passes every suite on kw.
- [ ] `BUILD-STATUS.md` records what the suites proved, committed as `docs: applications proven on kw`.

## Evidence

<!-- Filled at close time. -->
