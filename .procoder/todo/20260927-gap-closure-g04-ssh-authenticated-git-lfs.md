# Gap closure G04: SSH authenticated Git LFS

Status: open
Created: 2026-09-27

## Description

Implement the standard git-lfs-authenticate SSH command, returning a scoped,
short-lived credential for HTTPS batch/object transfer. Preserve existing actor,
repository and operation authorization on every request.

## Acceptance criteria

- [ ] An unmodified git-lfs client pushes and clones an SSH remote using verified HTTPS for payloads.
- [ ] Credentials expire and cannot be reused for Git, another repository, or another operation.
- [ ] Identity/grant revocation and archive rules apply at object transfer time.
- [ ] Chart/configuration supplies the advertised HTTPS endpoint; GUI explains SSH LFS support.
- [ ] The deployed transport passes the cluster acceptance case.

## Evidence

- Protocol checked against https://github.com/git-lfs/git-lfs/blob/main/docs/api/server-discovery.md on 2026-09-27: command accepts path and upload/download; response uses href, header and expires_in.
