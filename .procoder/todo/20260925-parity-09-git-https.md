# HTTPS for the Git transport (S-29)

Status: open
Created: 2026-09-25

## Description

Git is served over plain HTTP on :8081, so a clone or push sends its credential
across the network in the clear. That is acceptable only while nothing outside the
cluster uses it.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [ ] The `deploy` e2e clones and pushes over `https://` against the served certificate on
      the cluster.
- [ ] A plaintext HTTP request on the TLS port is refused rather than silently downgraded,
      asserted by the same suite.
- [ ] A cert-manager Certificate for the git-platform service name is issued by the
      cluster issuer and mounted read-only into the pod.
- [ ] `NF_GIT_TLS_CERT_FILE` and `NF_GIT_TLS_KEY_FILE` are added to the config, the chart
      and the service template, and are read.
- [ ] The plaintext listener remains available to in-cluster callers so nothing already
      working breaks.

## Evidence

<!-- Filled at close time. -->
