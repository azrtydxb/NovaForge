# Import and mirroring (S-27)

Status: closed 2026-09-25
Created: 2026-09-25

## Description

There is no way to bring an existing repository in from Gitea or GitHub, so there is
no migration path onto the platform at all.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [x] `internal/gitops TestImportFromRemote` passes: an imported repository has the
      remote's commits, branches and tags, and an import from an unreachable remote leaves
      no repository row and no directory.
- [x] `TestMirrorRefresh` passes: a mirror picks up a new upstream commit when the
      mirrorer runs, and a push to a mirror is refused naming upstream as the owner of its
      history.
- [x] The remote credential is encrypted under the service KEK, never in a stored remote
      URL in plain text and never in a log, proven by a test that greps the stored row and
      the captured log.
- [x] Import clones into a throwaway directory moved into place only once complete, so a
      failed import leaves nothing behind.
- [x] The mirrorer is started by `cmd/git-platform` and a test asserts it.

## Evidence

All runs against a disposable database created and dropped by
`hack/owned-db-test.sh`, because the shared dev datastore is written by every
other suite at the same time.

- `TestImportFromRemote` PASS (1.37s). Asserts the imported repository carries the
  remote's commits, branches and tags, and that a failed import leaves no row and
  no directory.
- `TestMirrorRefresh` PASS (1.75s). A new upstream commit appears after the
  mirrorer runs, and a push to a mirror is refused naming upstream.
- `TestMirrorCredentialIsEncryptedAndNeverLogged` PASS (0.87s). Greps the stored
  `remote` and `credential` columns, the value read back through `GetMirror`, the
  captured mirrorer log on a failing refresh, and the recorded `last_error`. The
  credential appears in none of them.
- Criterion 4 verified by breaking it, not by reading it: removing
  `defer os.RemoveAll(tmp)` from `importRepo` turned the test red with
  "a failed import left .import-971d822a-….git behind". Restored, green again.
  This matters because the agent's first version of that assertion passed
  vacuously — git removes its own target directory when a clone fails, so an
  implementation with no cleanup at all looked clean. What catches the leak is the
  second failure case, where the clone succeeds and the repositories insert is
  then refused as a duplicate name.
- Criterion 5 verified by breaking it: replacing the `go mirrorer.Run(ctx)`
  goroutine in `cmd/git-platform/main.go` with `_ = mirrorer` turned
  `TestGitPlatformStartsTheMirrorer` red with "cmd/git-platform never calls
  (*gitops.Mirrorer).Run: no mirror would ever be refreshed". The assertion
  type-checks the package rather than grepping it, so a comment or another type's
  Run would not satisfy it. Restored, green again.
- `gofmt -l internal cmd` prints nothing; `go build ./...` and `go vet ./...` exit 0;
  `go test ./...` reports no failures.

### Not proven, and why

- **Nothing here has run on the cluster.** The Nexus registry's write path is
  returning HTTP 500 to every blob upload (`POST /v2/novaforge/*/blobs/uploads/`
  → `{"errors":[{"code":"UNKNOWN"}]}`), so no image could be pushed at this commit
  and the deployment still runs `b84ffec`. Reads from the registry are fine. This
  is the registry host, not NovaForge code.
- **S-27 is only partly met in a default deployment.** `git-platform` is listed in
  the chart's `networkPolicy.airGapped`, so its egress is cluster-only: an import
  from GitHub or a public Gitea fails at connect until an operator removes it from
  that list. Import from an in-cluster remote works. `values.yaml` was
  deliberately not changed — that is the operator's security trade-off to make.
- No e2e script covers import or mirroring, and `nf` has no import command.
