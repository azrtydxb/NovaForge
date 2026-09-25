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
- [x] A cert-manager Certificate for the git-platform service name is issued by the
      cluster issuer and mounted read-only into the pod.
- [x] `NF_GIT_TLS_CERT_FILE` and `NF_GIT_TLS_KEY_FILE` are added to the config, the chart
      and the service template, and are read.
- [x] The plaintext listener remains available to in-cluster callers so nothing already
      working breaks.

## Evidence

Verified by me, not taken from the agent's report:

- `go test ./internal/gitops/ -run 'TLS|HTTPS|Certificate|Keypair|ChartTLS'` — six tests
  PASS: clone and push over HTTPS with a real git client verifying the chain; clone by
  the certificate's DNS name via a resolve override; plaintext on the TLS port refused;
  an unreadable keypair refused at construction; a renewed certificate served; the
  chart's published TLS port matching the served one.
- Red-green on the claim that mattered: forcing the keypair to load once made
  `TestTLSServerPicksUpARenewedCertificate` fail with "certificate signed by unknown
  authority" — the server kept presenting the pre-renewal pair. cert-manager renews in
  place, so a load-once process would serve an expired certificate until something
  restarted it, and nothing would. Restored and green.
- `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` clean after merging into
  main alongside the collaborator work; `go test ./internal/service/ ./internal/authz/`
  ok.
- The agent reported using two guards this tree already had as its own red proofs:
  `TestLoadConfigPopulatesEveryField` caught the config fields declared but not read,
  and `chart_test.go` — which finds reads by type-checking the binary — caught the
  Deployment not setting them.

NOT met, and not checked off:

- The first two criteria ask the `deploy` e2e to clone and push over HTTPS on the
  cluster and to assert the plaintext refusal there. The step is written but has NOT
  been executed: no images were built and nothing was deployed. The equivalent
  behaviour is proven in-process only.
- That the Certificate is actually issued by `cluster-ca` and mounted into the pod is
  proven only as far as the chart renders.
- The e2e reads `ca.crt` from the issued Secret, which is assumed to be populated the
  way it is for the OpenBao deployment. Unconfirmed.
