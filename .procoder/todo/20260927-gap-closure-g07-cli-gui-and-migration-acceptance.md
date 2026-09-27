# Gap closure G07: CLI GUI and migration acceptance

Status: closed 2026-09-27
Created: 2026-09-27

## Description

Complete CLI fork/import/mirror and cross-fork Run commands, document migration fidelity, and prove the combined GUI and cluster workflows with real users.

## Acceptance criteria

- [x] CLI creates forks, imports, inspects/refreshes/stops mirrors and opens cross-fork Runs.
- [x] Upstream credentials are read from stdin or a file, not command arguments or output.
- [x] GUI shows useful clone, migration and policy information; creation/failure/recovery works.
- [x] Cluster acceptance covers webhooks, SSH LFS, forks, mirrors and cleanup.
- [x] Documentation states which data is imported and which needs separate migration.

## Evidence

- nf fork/import/mirror status/refresh/stop and Run --source-repo are implemented. Import credentials use a file or stdin rather than secret argv/URLs.
- Deployed git_host acceptance at ba12eff passed CLI import, refresh after upstream commit, mirror conversion, fork and cross-fork Run creation, signed notifications, SSH LFS and actual blob cleanup.
- Actual browser checks passed policy-refused import, correcting the URL and importing a mirror, refresh, stop following upstream, fork creation, and cross-fork Changes/impact rendering. Browser fork evidence is retained in /tmp/novaforge-gap-20260927/browser-fork.txt.
- GUI and docs/gap-closure-operations.md explicitly distinguish Git refs/history from LFS payloads, releases, users, issues and CI metadata. No unsupported full-host migration claim is made.
