# Gap closure G06: approved outbound destinations

Status: closed 2026-09-27
Created: 2026-09-27

## Description

Add explicit operator destination rules for Git imports/mirroring and webhook delivery, enforced before network access and in Cilium. Keep default external egress closed.

## Acceptance criteria

- [x] Only approved scheme/host/port and resolved address combinations can connect; redirects and inherited proxies are refused.
- [x] Git and HTTP pin checked addresses, including IPv6 handling; metadata/link-local/control-plane addresses are refused.
- [x] Chart renders matching destination egress without removing default air-gap policy.
- [x] Approved external fixture import, refresh and signed delivery pass on kw; unapproved destinations fail.
- [x] Policy errors remain distinguishable in API and GUI.

## Evidence

- Exact scheme/host/port/CIDR policy validates DNS answers and pins HTTP/Git connections. Ambient proxies and redirects are disabled. Loopback/link-local/non-unicast addresses are refused; a public wildcard does not permit private addresses.
- Cilium rules render the same approved destination hosts/ports while preserving default air-gap selectors. Private fixture access uses its exact /32; github.com is the separately approved public host.
- Real policy and poisoned-proxy connection tests passed. Deployed ba12eff git_host acceptance passed controlled upstream import/refresh, GitHub import and signed hook delivery; metadata import was refused before network access.
- Browser import displayed the operator-policy error, then accepted the corrected approved upstream. docs/gap-closure-operations.md documents exact configuration, fidelity and rollback to an empty allowlist.
