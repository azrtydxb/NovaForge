# Deploy NovaForge on OKE at novaforge.azrty.com with Zitadel sign-in

Status: open
Created: 2026-10-21

## Description

NovaForge runs on kw (lab). It must also run on the azrty OKE cluster at
https://novaforge.azrty.com, onboarded the way every azrty tenant is (atlas is the
reference), with people signing in through the cluster's Zitadel
(login.azrty.com). OKE's CNI (flannel) enforces no NetworkPolicy, so features that
run untrusted repository code stay off there until isolation is enforced.

## Acceptance criteria

- [ ] OIDC SSO: public client + PKCE; ID token verified (signature via JWKS, iss,
      aud, nonce, time); users linked by (issuer, subject) only; tests seen red
      for each verification step; identity RPC refuses every caller but the edge.
- [ ] OKE chart profile renders: HTTPRoute (Git rule before catch-all), shared-pg
      DSN, OCIR images, no cluster-scoped objects; kw render changes only by the
      reviewed lines.
- [ ] Images publish from CI to OCIR on arm64 and the tag is pinned without a
      publish loop.
- [ ] azrty-infra onboarding merged: OCIR repos, DNS, listener + cert, Zitadel
      app, shared-pg role + database (pgvector), tenant.
- [ ] On the cluster: https://novaforge.azrty.com serves the app over a valid
      certificate; "Sign in with azrty" completes a real Zitadel sign-in and lands
      in the app; a git clone and push over https://novaforge.azrty.com works.
- [ ] Code-executing features are verifiably off on OKE and say so (no silent
      queue).

## Evidence

<!-- Filled at close time. -->
