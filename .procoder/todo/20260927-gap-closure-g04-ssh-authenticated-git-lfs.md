# Gap closure G04: SSH authenticated Git LFS

Status: closed 2026-09-27
Created: 2026-09-27

## Description

Implement the standard git-lfs-authenticate SSH command, returning a scoped,
short-lived credential for HTTPS batch/object transfer. Preserve existing actor,
repository and operation authorization on every request.

## Acceptance criteria

- [x] An unmodified git-lfs client pushes and clones an SSH remote using verified HTTPS for payloads.
- [x] Credentials expire and cannot be reused for Git, another repository, or another operation.
- [x] Identity/grant revocation and archive rules apply at object transfer time.
- [x] Chart/configuration supplies the advertised HTTPS endpoint; GUI explains SSH LFS support.
- [x] The deployed transport passes the cluster acceptance case.

## Evidence

- Standard SSH git-lfs-authenticate returns an encrypted five-minute ticket scoped to organization, immutable repository identity and upload/download operation. Each HTTP use re-resolves the key/actor; plaintext ticket use and oversized authorization headers are refused.
- TestLFSOverSSHWithVerifiedHTTPS passed with an unmodified client, including wrong-repository, wrong-operation, ordinary-Git misuse and revoked-key refusal. TestLFSExpiredTicketAndPlaintextRefused passed.
- kw git_host acceptance on ba12eff passed random 2 MiB SSH LFS push/clone, fork clone and archived-upload refusal over trusted-CA HTTPS. The advertised public endpoint and certificate IP SAN agree.
- Actual browser clone guidance explains SSH authentication plus HTTPS payload transfer and CA trust. Evidence: /tmp/e2e.git_host.log and targeted-release-acceptance.log.
