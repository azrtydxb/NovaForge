# Gap closure G09: governed deployment production integration

Status: closed 2026-09-27
Created: 2026-09-27

## Description

Wire the existing governed-deployment service and qualify a real provider-backed, target-expiring credential path. Build an immutable runner/chart fixture and exercise it through REST and GUI on kw.

## Acceptance criteria

- [x] Gates starts the deployment service, migration, cleanup and success outbox; edge/GUI expose the supported lifecycle.
- [x] Qualified Kubernetes credentials are minted by OpenBao, validated against the target and bounded by signed token expiry; unqualified providers remain refused.
- [x] Approved fixed chart/image creates an observed workload at the configured target.
- [x] Independent approval, denial, expiry, changed intent, failure/retry and duplicate execution protections are proven.
- [x] Credentials are revoked/fenced and deployment evidence reaches the graph.

## Evidence

Production gates/REST/GUI integration is deployed on kw at application 19b29cc, Helm revision 16. The separately pinned c9b6778 operator runner contains the approved chart; its image/chart checksums are recorded in .procoder/evidence/gap-closure-20260927.md.

The governed_deploy cluster suite passed actual workload creation, independent approval, self-approval and denial refusal, changed-intent refusal, replay without duplicate execution, real target authorization failure followed by explicit retry, generated credential/account cleanup and observed attempt-2 graph provenance. The separate real 600-second OpenBao Kubernetes credential probe observed target authentication succeed and then become Unauthorized after signed expiry. Unqualified providers remain refused.

Actual browser request f7a40bff-278f-4fd0-ad43-fbd7953bb015 was created by the author, approved by the independent reviewer after inspecting exact artifact/destination/revision/request, and executed successfully by the author. The GUI displays attempt 1 succeeded. A stale earlier approval was correctly refused after a newer destination operation. Earlier ambiguous fixture operations retain uncertain history with resolved credentials. Browser evidence is /tmp/novaforge-gap-20260927/browser-deployment.txt.

Full Go regression and subsequent deployment/edge/secrets regressions pass; all 32 existing browser regression cases pass. Real production target onboarding remains explicit operator configuration; qualification resources are disposable and covered by G12 cleanup.
