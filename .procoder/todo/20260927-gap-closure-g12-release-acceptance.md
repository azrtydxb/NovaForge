# Gap closure G12: release acceptance

Status: open
Created: 2026-09-27

## Description

Verify and publish the completed authorized release, retaining honest evidence
and explicit broader follow-ups.

## Acceptance criteria

- [ ] Full Go regression, relevant failure cases, build/vet/format, frontend and generated-contract checks pass.
- [ ] Immutable images are deployed through Helm and original plus new registered cluster suites pass.
- [ ] Changed browser workflows work with actual users and repositories.
- [ ] Disposable credentials/configurations are cleaned up and the installation is healthy.
- [ ] Traceability, build status and task evidence reflect actual deployed behavior; tasks close through procoder.
- [ ] Verified commits are published to the refreshed remote.

## Evidence

Execution logs are collected under `/tmp/novaforge-gap-20260927/` until the final
durable checkpoint is written.
