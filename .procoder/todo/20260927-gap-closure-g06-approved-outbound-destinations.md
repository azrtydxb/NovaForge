# Gap closure G06: approved outbound destinations

Status: open
Created: 2026-09-27

## Description

Add explicit operator destination rules for Git imports/mirroring and webhook delivery, enforced before network access and in Cilium. Keep default external egress closed.

## Acceptance criteria

- [ ] Only approved scheme/host/port and resolved address combinations can connect; redirects and inherited proxies are refused.
- [ ] Git and HTTP pin checked addresses, including IPv6 handling; metadata/link-local/control-plane addresses are refused.
- [ ] Chart renders matching destination egress without removing default air-gap policy.
- [ ] Approved external fixture import, refresh and signed delivery pass on kw; unapproved destinations fail.
- [ ] Policy errors remain distinguishable in API and GUI.

## Evidence

- Git's official git-config documentation confirms http.curloptResolve pins HOST:PORT to supplied addresses; verified 2026-09-27.
