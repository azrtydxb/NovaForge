# Gap closure G08: cert-manager rotation acceptance

Status: open
Created: 2026-09-27

## Description

Run the deployed git-platform binary with a dedicated cert-manager Certificate, induce reissuance and observe the mounted Secret reaching fresh TLS handshakes without a restart. Keep the shared issuer unchanged.

## Acceptance criteria

- [ ] Dedicated certificate is reissued by cert-manager, with a changed serial.
- [ ] A verified TLS connection serves the new serial without changing pod UID/restart count.
- [ ] Standard Git clone/push succeeds before and after rotation; plaintext remains refused.
- [ ] Dedicated certificate, Secret, service and pod are removed after the test.

## Evidence

