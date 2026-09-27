# Qualify and activate the approved Nexus PVC trim

Status: closed 2026-09-27
Created: 2026-09-27

## Description

The user's follow-up instruction authorizes progressing the existing exact-scope
Nexus maintenance proposal. Do not expand to host-wide or privileged maintenance.

## Acceptance criteria

- [x] Revalidate exact PVC/node/image and capture workload/space baseline.
- [x] Apply the suspended bounded CronJob and qualify its command on only that PVC.
- [x] Record filesystem discard and backing allocation evidence plus Nexus availability; retain uncertainty where measurements cannot establish reclaimed allocation.
- [x] Activate weekly scheduling only after qualification, preserving deadline/concurrency limits and a documented suspend/delete rollback.

## Evidence

Actual kw Job nexus-trim-qualification-20260927 completed with exit 0, reported
10,810,601,472 bytes trimmed, and ZFS backing allocation decreased 5,238,333,360
bytes. Nexus remained 2/2 ready with zero restarts; HTTP status passed in 2 ms.
Weekly Sunday 00:00 UTC schedule is active with the original bounded privileges,
Forbid concurrency and 300-second deadline. Exact scope, raw measurements,
limitations and rollback: .procoder/evidence/nexus-trim-20260927.md.
The procoder gate passed with zero blocking findings.
