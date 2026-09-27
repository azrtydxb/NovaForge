# Gap closure G08: cert-manager rotation acceptance

Status: closed 2026-09-27
Created: 2026-09-27

## Description

Run the deployed git-platform binary with a dedicated cert-manager Certificate, induce reissuance and observe the mounted Secret reaching fresh TLS handshakes without a restart. Keep the shared issuer unchanged.

## Acceptance criteria

- [x] Dedicated certificate is reissued by cert-manager, with a changed serial.
- [x] A verified TLS connection serves the new serial without changing pod UID/restart count.
- [x] Standard Git clone/push succeeds before and after rotation; plaintext remains refused.
- [x] Dedicated certificate, Secret, service and pod are removed after the test.

## Evidence

- kw deployment 9733e2d, normal Helm rollout, 2026-09-27.
- tests/e2e/cert_rotation_test.sh passed via hack/e2e-in-cluster.sh; log /tmp/e2e.cert_rotation.log and /tmp/novaforge-gap-20260927/core-acceptance-recheck.log.
- Dedicated cert-manager certificate changed serial 1D9022CA58808086A5B62EBBA2C43DE8 → 5206B439A54A06604EE94A1291DC212B. The production TLS listener served both with CA/hostname verification; pod UID/restart count remained unchanged during renewal.
- Real Git clone/push before and after renewal passed, plaintext was refused, and the finally block removed the dedicated pod/service/certificate/Secret and test organization.
