# Offline advisory coverage and refresh

Dependency-change approval and vulnerability scanning answer different questions.
Approval detects changes to go.mod, package.json, Cargo.toml and requirements*.txt.
The vulnerability gate uses the pinned OSV scanner's resolved package evidence
and the immutable analysis image's advisory databases. A recognized approval
manifest is not proof that the scanner extracted its dependencies.

| Ecosystem        | Qualified resolved inputs                | Image advisory database                |
| ---------------- | ---------------------------------------- | -------------------------------------- |
| Go               | go.mod / go.sum with concrete versions   | Go                                     |
| npm              | package-lock.json with concrete versions | npm                                    |
| Python           | requirements.txt with exact versions     | PyPI                                   |
| Rust             | Cargo.lock with concrete versions        | crates.io                              |
| Other ecosystems | Not covered by the default image         | Absent; extracted packages fail closed |

Ranges, unpinned requirements and bare package.json/Cargo.toml files are not an
assertion of complete coverage. An empty package result is unavailable, never a
clean verdict. Transitive completeness depends on the actual resolved inputs.
All four rows were exercised on September 27 against the immutable production
analysis image in real isolated cluster workspaces. The fixtures used
golang.org/x/text 0.3.0, lodash 4.17.4, requests 2.19.1 and time 0.1.42,
respectively. Each produced actual vulnerability findings. Missing, expired and
corrupted snapshots were refused. See
[qualification evidence](../.procoder/evidence/intelligence-20260927.md).

The image manifest requires exactly schema_version, generated_at, valid_until and
ecosystems. Each represented ZIP has a SHA-256 digest. Before scanning, the gate
checks the trusted clock, validity interval, file size, symlink ancestors and
all digests. It rechecks the snapshot afterward. Corrupt, absent, expired,
future-dated and malformed data fail closed. A scanned ecosystem absent from the
manifest also fails closed. Repository OSV suppression/config files cannot select
another database or suppress advisories.

To refresh production:

1. Update `deploy/analysis/advisory-refresh.txt` with the review date/reason and
   commit it. Its COPY instruction invalidates the advisory download layer;
   rebuilding service code alone can reuse an old layer.
2. Build `gate-analysis`, `gates` and `work-reviews` through `hack/build-images.sh`
   on kw. The Dockerfile downloads and validates the four ZIPs, records their
   digests and a 90-day validity interval. Never extend a manifest's expiry without
   refreshing and validating its databases.
3. Retain image digest, manifest and real vulnerable/clean fixture results for
   every supported ecosystem. Run damaged/missing/expired negative regressions.
4. Build the remaining release services and deploy through `hack/deploy.sh` with
   the immutable commit tag; it resolves and installs the sandbox digest. Verify
   the live dependencies gate and record the deployment revision.

Refresh before the validity deadline, and sooner when new advisories require it.
`hack/stage-advisories.sh` stages only the workstation's Go test fixture; it does
not update production. To refresh that fixture, move aside the existing cached
Go-all.zip, rerun the staging script, and rerun the offline regressions.
