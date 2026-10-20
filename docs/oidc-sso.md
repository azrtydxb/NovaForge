# Single sign-on (OpenID Connect)

NovaForge can sign people in through an OpenID Connect provider — for the
azrty deployment, the organization's Zitadel instance at
`https://login.azrty.com`. Password sign-in can stay alongside it or be turned
off.

## What the platform team provisions

A **public** OIDC client, exactly like the existing `atlas` application:

| Setting                   | Value                                                   |
| ------------------------- | ------------------------------------------------------- |
| Grant                     | Authorization code                                      |
| PKCE                      | Required, `S256`                                        |
| Client secret             | **None** (public client; auth method `none`)            |
| Redirect URI              | `https://novaforge.azrty.com/api/v1/auth/oidc/callback` |
| Scopes NovaForge asks for | `openid email profile`                                  |

And a Kubernetes Secret in the NovaForge namespace:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: novaforge-oidc
stringData:
  OIDC_ISSUER: https://login.azrty.com
  OIDC_CLIENT_ID: "<the client id Zitadel assigned>"
  OIDC_REDIRECT_URI: https://novaforge.azrty.com/api/v1/auth/oidc/callback
```

There is no client secret to store, so none is read. The redirect URI in the
Secret must be byte-for-byte the one registered at the provider.

## Chart values

```yaml
oidc:
  existingSecret: novaforge-oidc
  issuerHost: login.azrty.com # the edge's only egress outside the cluster
  issuerPort: 443
  buttonLabel: "Sign in with azrty"
  passwordLoginEnabled: true # false = SSO only
```

- `existingSecret` empty (the default) leaves SSO off and changes nothing.
- The edge is air-gapped by `networkPolicy.airGapped`; with SSO on it may also
  reach `issuerHost:issuerPort` (discovery, signing keys, code exchange). The
  chart refuses to render SSO without `issuerHost` rather than deploy an edge
  that cannot reach its provider.
- `passwordLoginEnabled: false` makes identity refuse password `Login` and
  `Register` (not only the edge), and the login page draws no password form.
  The chart refuses it without `existingSecret`, since nobody could sign in.
  The `nf` CLI signs in with a password; with password sign-in off, CLI users
  authenticate with a personal access token created in the GUI.
- The edge refuses to start with only some of the three `OIDC_*` keys set.

## How a sign-in works

1. `GET /api/v1/auth/oidc/start` — the edge discovers the provider
   (`{issuer}/.well-known/openid-configuration`, cached after the first
   success), creates a `state`, a `nonce` and a PKCE verifier, and redirects to
   the provider's authorization endpoint with the S256 challenge.
2. `GET /api/v1/auth/oidc/callback` — the edge checks `state`, redeems the code
   with the verifier (no secret), and verifies the ID token with go-oidc: the
   signature against the provider's JWKS (refetched when a `kid` is unknown, so
   key rotation is followed), `iss` equal to the issuer, `aud` containing the
   client id, `exp`; then `nonce`, and `iat`/`nbf` within one minute of skew
   (`iat` no older than ten minutes).
3. Identity links or provisions the user and mints an ordinary session.
4. The browser is sent to `/?oidc=complete`; the page collects the session with
   `POST /api/v1/auth/oidc/session`.

Every failure redirects to `/?oidc_error=<code>` — `not_configured`,
`provider_unavailable`, `denied`, `bad_state`, `exchange_failed`,
`invalid_token`, `access_denied`, `signin_failed` — and the reason is logged by
the edge (`edge: oidc …`). No error page, no token, no provider text in the URL.

`GET /api/v1/auth/oidc/config` tells the login page what to draw:
`{enabled, issuer, button_label, password_login_enabled}`.

### Why the state lives in a cookie

The edge owns no datastore and runs as several replicas, so the callback can
reach a replica that never saw the start. `state`, `nonce` and the PKCE
verifier are therefore kept in a short-lived (10 minute) cookie that is
HttpOnly, SameSite=Lax (the callback is a top-level navigation from the
provider, which Strict would strip), Secure and `__Host-`-prefixed on https,
and MACed with a key derived from `HMAC_SECRET`. The callback accepts it only
when its `state` equals the one the provider returned, and spends it. There is
one such cookie per browser: starting sign-in in a second tab replaces the
first tab's, whose return then fails with `bad_state`.

### Why the session is handed over by cookie, not URL

A token in a query string lands in access logs, browser history and `Referer`
headers; a fragment still lands in history. The callback instead sets a
two-minute, HttpOnly, sealed hand-off cookie and redirects to a URL that
carries only `oidc=complete`. The page exchanges the cookie for the session
token with one same-origin POST, and the edge clears it. The hand-off is
stateless: it is one-shot because the browser drops it, and a copied value is
the session itself — no more exposure than the `nf_session` cookie password
sign-in already sets.

## Who someone is: the linking rule

An external identity is linked to a NovaForge user by **(issuer, subject)
only**, in `identity.external_identities`. It is never linked by username or by
email — verified or not — because whoever controls those claims at the
provider would then be handed the NovaForge account that shares them.

- First sign-in provisions a user (just-in-time). Its username is
  `preferred_username` (the local part, if it is an email-style login name)
  lowercased and reduced to `[a-z0-9._-]`, at most 39 characters.
- If that username is taken — by a local account or another linked one — the
  new user gets a deterministic suffix derived from (issuer, subject), e.g.
  `pascal-3f9a1c`. The existing account is untouched and keeps signing in with
  its password.
- The email is recorded only if the provider marked it verified and no other
  account holds it; otherwise the new account has none (it is never invented).
- A provisioned user belongs to **no organization**. Organizations remain the
  security boundary: an owner or admin invites the user (by username) before
  they can see anything. NovaForge does not read the provider's role claims.
- There is no self-service way to attach an existing password account to an
  external identity. Doing that safely needs a flow where the person proves
  both — signed in with the password, then completing SSO — which is not built.
- Two-factor: SSO relies on the provider's own MFA. NovaForge's TOTP guards
  password sign-in only.

## Authorization of the identity RPC

`IdentityService.SignInExternal` trusts what it is given, because the edge has
verified the ID token. That is why only the edge may call it: identity admits
only a platform token (`svcauth.MintPlatform`) naming `edge-oidc`, signed with
the deployment's `HMAC_SECRET`. A signed-in user, an org-scoped service token
and any other platform worker are refused with `PermissionDenied`.
