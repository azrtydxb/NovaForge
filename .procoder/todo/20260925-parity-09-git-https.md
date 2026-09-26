# HTTPS for the Git transport (S-29)

Status: closed 2026-09-26
Created: 2026-09-25

## Description

Git is served over plain HTTP on :8081, so a clone or push sends its credential
across the network in the clear. That is acceptable only while nothing outside the
cluster uses it.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [x] The `deploy` e2e clones and pushes over `https://` against the served certificate on
      the cluster.
- [x] A plaintext HTTP request on the TLS port is refused rather than silently downgraded,
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

### Closed on the cluster (2026-09-26)

The first two criteria were open only because nothing had been deployed — the images
could not be pushed while the Nexus blob store was full (recorded in
`.procoder/ask/decisions.md` and BUILD-STATUS). With that fixed, the platform was
deployed at `dc5f70f` (helm revision 9) and the step ran:

```
== 4b. the same clone and push work over HTTPS, and plaintext on the TLS port is refused ==
Cloning into '/tmp/tmp.6EU2LjtqoY/repo'...
To https://novaforge-git-platform.novaforge.svc:8443/e2eorg3246489/widgets15447.git
   f631db0..1e67201  HEAD -> main
ok: cloned and pushed 1e67201c710103d68d27c581763663ee29ed6e9a over HTTPS against the served certificate
ok: plaintext on the TLS port is refused
```

That settles the two open criteria and the two doubts recorded under them: the
certificate IS issued and mounted, because a real git client verified the chain
against the certificate's own DNS name with `sslVerify` on, and the issued Secret's
`ca.crt` IS populated, because the suite read it from the Secret and the verification
succeeded. Run via `hack/e2e-in-cluster.sh deploy`; the full per-step log is the
runner's own, not a reconstruction.

All 13 in-cluster suites pass at this revision: airgap, deploy, work_ci, secrets, gui,
search, graph, factory, agent, agent_ci, merge, cli, crossorg.

### Still not proven

- **Certificate renewal has not been observed in a pod.** The Go test proves the
  server reloads a renewed keypair without a restart, and that test was seen red with
  a load-once keypair. But no deployment has yet lived through cert-manager actually
  rotating the certificate, so the interaction between the two is still inference.
- **LFS does not work over this transport's SSH counterpart** (S-25), unrelated to TLS
  but worth stating next to it: there is no `git-lfs-authenticate` over SSH.
