# Gap closure G07: CLI GUI and migration acceptance

Status: open
Created: 2026-09-27

## Description

Complete CLI fork/import/mirror and cross-fork Run commands, document migration fidelity, and prove the combined GUI and cluster workflows with real users.

## Acceptance criteria

- [ ] CLI creates forks, imports, inspects/refreshes/stops mirrors and opens cross-fork Runs.
- [ ] Upstream credentials are read from stdin or a file, not command arguments or output.
- [ ] GUI shows useful clone, migration and policy information; creation/failure/recovery works.
- [ ] Cluster acceptance covers webhooks, SSH LFS, forks, mirrors and cleanup.
- [ ] Documentation states which data is imported and which needs separate migration.

## Evidence

