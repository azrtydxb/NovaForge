# applications 12: GUI — Applications

Status: open
Created: 2026-10-21

## Description

Task 12 of `.procoder/plans/applications.md` (spec `.procoder/specs/applications.md`): GUI — Applications. Done when the task's steps are carried out exactly as the plan writes them, its tests were seen red then green, and nothing outside the task's Files list changed except what its steps name.

## Acceptance criteria

- [ ] `web/tests/applications.test.mjs` exists and `cd web && npx tsc -b --noEmit` failed with `Cannot find module './screens/Applications'` before the screens were written.
- [ ] `cd web && npx tsc -b --noEmit && npm run build` exits 0; the Rail shows Applications under BUILD between Repositories and Work; screens use `Failed` for errors and render no invented data.
- [ ] The browser scenario `npm test -- applications` passes against kw after Task 14.
- [ ] The procoder commit gate passes; the change is committed as `feat(web): applications, change sets and composite releases`.

## Evidence

<!-- Filled at close time. -->
