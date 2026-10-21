# Deploy NovaForge on OKE at novaforge.azrty.com with Zitadel sign-in

Status: closed 2026-10-21
Created: 2026-10-21

## Description

NovaForge runs on kw (lab). It must also run on the azrty OKE cluster at
https://novaforge.azrty.com, onboarded the way every azrty tenant is (atlas is the
reference), with people signing in through the cluster's Zitadel
(login.azrty.com). OKE's CNI (flannel) enforces no NetworkPolicy, so features that
run untrusted repository code stay off there until isolation is enforced.

## Acceptance criteria

- [x] OIDC SSO: public client + PKCE; ID token verified (signature via JWKS, iss,
      aud, nonce, time); users linked by (issuer, subject) only; tests seen red
      for each verification step; identity RPC refuses every caller but the edge.
- [x] OKE chart profile renders: HTTPRoute (Git rule before catch-all), shared-pg
      DSN, OCIR images, no cluster-scoped objects; kw render changes only by the
      reviewed lines.
- [x] Images publish from CI to OCIR on arm64 and the tag is pinned without a
      publish loop.
- [x] azrty-infra onboarding merged: OCIR repos, DNS, listener + cert, Zitadel
      app, shared-pg role + database (pgvector), tenant.
- [x] On the cluster: https://novaforge.azrty.com serves the app over a valid
      certificate; "Sign in with azrty" completes a real Zitadel sign-in and lands
      in the app; a git clone and push over https://novaforge.azrty.com works.
- [x] Code-executing features are verifiably off on OKE and say so (no silent
      queue).

## Evidence

- OIDC: fresh runs on 2026-10-21: `go test -count=1 ./internal/edge -run OIDC` ok (6 OIDC tests pass), `./internal/identity -run External` ok, `./internal/svcauth` ok. Each verification step (signature, iss, aud, nonce, time, PKCE, (issuer, subject) linking, edge-only RPC) was seen red when built (commit 6f38203). Live: the authorize redirect carries `code_challenge_method=S256` and the public client_id, and Zitadel shows its login page for it.
- Chart: `go test -count=1 ./internal/service -run 'OKE|Kw|OIDC|ScientificNotation'` → 13 PASS, including TestOKEHTTPRouteSendsGitToGitPlatformBeforeTheCatchAll, TestOKERenderIsDeployableByANamespaceScopedOfflineRenderer (no cluster-scoped objects), TestOKEReachesOCIObjectStorageNotABundledMinIO and TestKwRenderIsUnchanged (the kw golden diff, reviewed, changes only Redis's securityContext and its /data emptyDir).
- Publish: the "Publish to OKE" run for 2161003 succeeded (12 arm64 images to dxb.ocir.io/axmcgpqk0w0z/azrty/novaforge-*). Pin PR #25 was opened by the app and auto-merged after `test`, and the pin merge started no publish (paths-ignore).
- Infra: azrty-infra #135 (OCIR repos, route53, listener + certificate, Zitadel app, shared-pg role + database with vector, tenant), #136 (OCI Object Storage buckets and key) and #137 (4th platform node) are merged. Sync reports tenant-novaforge and novaforge Healthy/Healthy.
- Live: `curl https://novaforge.azrty.com/` → 200 with a Let's Encrypt certificate (CN=novaforge.azrty.com, valid until 2027-01-19). The user signed in with Zitadel, and identity.external_identities links `pascal.watteel` to https://login.azrty.com (2026-10-21 09:17 UTC). A git clone, commit and push over https://novaforge.azrty.com, then a fresh clone, returned the commit; a clone without credentials is refused (401). The test org was deleted. SSO-only: register → 403, password login → 403.
- Code execution off: there is no novaforge-runner Deployment; gates logs "NF_GATE_ANALYSIS_IMAGE is unset; executable gates are unavailable"; agent-runtime logs "workspace provisioning and the reaper are disabled", and StartRun then refuses with FailedPrecondition (internal/agents/grpc.go:306) rather than queueing. No Role or ClusterRole bindings exist for NovaForge's ServiceAccounts (`auth can-i create pods` → no). A pending CI job says no runner is connected (TestPendingJobSaysWhenNoRunnerIsConnected ok). TestOKEKeepsCodeExecutionOffWhileNetworkPolicyIsUnenforced passes.
