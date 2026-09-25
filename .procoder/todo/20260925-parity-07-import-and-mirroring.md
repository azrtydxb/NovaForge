# Import and mirroring (S-27)

Status: open
Created: 2026-09-25

## Description

There is no way to bring an existing repository in from Gitea or GitHub, so there is
no migration path onto the platform at all.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [ ] `internal/gitops TestImportFromRemote` passes: an imported repository has the
      remote's commits, branches and tags, and an import from an unreachable remote leaves
      no repository row and no directory.
- [ ] `TestMirrorRefresh` passes: a mirror picks up a new upstream commit when the
      mirrorer runs, and a push to a mirror is refused naming upstream as the owner of its
      history.
- [ ] The remote credential is encrypted under the service KEK, never in a stored remote
      URL in plain text and never in a log, proven by a test that greps the stored row and
      the captured log.
- [ ] Import clones into a throwaway directory moved into place only once complete, so a
      failed import leaves nothing behind.
- [ ] The mirrorer is started by `cmd/git-platform` and a test asserts it.

## Evidence

<!-- Filled at close time. -->
