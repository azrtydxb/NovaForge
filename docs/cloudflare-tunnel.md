# NovaForge Cloudflare tunnel

A public name for the platform, alongside the internal `*.kw.watteel.lab` names.
The connector is **operator-managed, not Sync-managed**: it lives in the
`novaforge-tunnel` namespace, which the Kuvryn Sync Application does not own, so
it never appears in `values-kw.yaml` and never fights the rendered release.

## What exists

- Namespace `novaforge-tunnel`, Deployment `novaforge-cloudflared` (2 replicas,
  digest-pinned image from the nexus mirror, non-root with `fsGroup 65532` —
  that group is what makes the `0440` token file readable; without it the
  connector CrashLoops with "permission denied" on its own token).
- Secret `novaforge-cloudflared-token` holds the tunnel token. It is an
  operator Secret created with `kubectl`, outside git and outside Sync, like
  the other operator secrets. The token transited chat when it was handed
  over; rotating it in the Cloudflare dashboard and re-creating the Secret is
  cheap hygiene.

The tunnel is token-run, so its public-hostname mapping lives in the Cloudflare
Zero Trust dashboard, not in any config file here:

| Public hostname                          | Service                                                          |
| ---------------------------------------- | ---------------------------------------------------------------- |
| `novaforge.<zone>` (GUI + REST API)      | `http://novaforge-edge.novaforge.svc.cluster.local:8080`         |
| `novaforge-git.<zone>` (Git HTTPS + LFS) | `http://novaforge-git-platform.novaforge.svc.cluster.local:8081` |

The git hostname is a **first-level** subdomain on purpose. Cloudflare's
Universal SSL certificate covers `<zone>` and `*.<zone>` only, so a two-level
name such as `git.novaforge.<zone>` resolves at the edge and then fails the TLS
handshake there — the mapping exists but no edge certificate serves it. Total
TLS or Advanced Certificate Manager would lift that limit; until then the
public git name is `novaforge-git.<zone>`.

The origin scheme is deliberately **http**: the tunnel encrypts connector to
edge, so pointing it at the plaintext in-cluster port is not a exposure, while
pointing it at `:8443` would fail origin SNI verification against the cluster
certificate's names.

## Internal names

Nexora serves `watteel.lab` internally: `novaforge.kw.watteel.lab` → edge,
`git.novaforge.kw.watteel.lab` → the git LoadBalancer. Both names are on the
git TLS certificate (`gitTLS.extraDNSNames` in `values-kw.yaml`) together with
the LoadBalancer IP SAN, so a client verifies whichever it dials. `lfs.publicURL`
advertises the name, never a bare address — an IP is never in the DNS SANs.
