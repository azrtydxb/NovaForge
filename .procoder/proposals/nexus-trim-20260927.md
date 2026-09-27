# Nexus trim proposal — 2026-09-27

This is a reviewable shared-infrastructure proposal, not an applied change.
The gap-closure plan G11 explicitly reserves this decision for the shared
infrastructure owner. NovaForge's implementation does not need it to proceed.

## Exact target and scope

- Cluster `kw`, namespace `nexus`, PVC `nexus-data-iscsi`.
- PV `pvc-1a9dcde3-a41e-49af-bdd1-05fcb922440f`, 1 TiB, ext4, RWO.
- Current Nexus pod: `nexus-5645c89dfb-z6dqx`, node `master-12`.
- Scheduled pod mounts only that PVC at `/nexus-data`, with required affinity to
  the Nexus pod's node. No hostPath, host PID/network or Kubernetes API token.
- Command: `fstrim -v /nexus-data`. No `--all`, no filesystem walk/delete.
- Proposed schedule: Sunday 00:00 UTC (04:00 Dubai), 300-second deadline, no
  concurrent jobs. The supplied manifest starts **suspended** for review.
- Privilege: root plus `CAP_SYS_ADMIN` for FITRIM, all other capabilities dropped,
  read-only container root. This is consequential filesystem authority even
  without Kubernetes `privileged: true`; approve it as such.

## Why scheduled trim

Scheduled trim returns already-free ext4 blocks to the thin-provisioned zvol.
It does not replace Nexus retention or blob compaction. Compared with adding
mount-time `discard`, a bounded job avoids changing the PV mount options and
remounting/restarting Nexus, and gives a discrete maintenance interval and logs.

Before activation, the owner should qualify FITRIM support and latency on this
exact PVC during the window, watch Nexus request latency, and confirm a decrease
in the zvol's allocated bytes. A successful command is not proof of recovered
ZFS pool space by itself. Do not broaden to a privileged host pod if the reduced
capability configuration fails; bring that concrete result back for review.

## Activation and rollback

1. Review `nexus-trim-20260927.yaml` and utility image digest.
2. Apply it suspended, then create one Job from it during the approved window.
3. Verify its exact mount and resulting Nexus/ZFS observations.
4. Unsuspend the weekly schedule only after that evidence passes.

Rollback: suspend/delete `nexus-volume-trim`; delete its active Job if necessary.
No application manifests, volume mount options or on-disk data format change.
A completed discard cannot be undone, but it concerns blocks already marked free
by ext4, not live Nexus blob data.

## Capacity evidence

Read-only `df -B1 /nexus-data` on 2026-09-27 reported total 1,081,109,192,704
bytes, used 230,503,055,360, available 804,440,813,568 (about 749.2 GiB free).
No CronJob exists in namespace `nexus`. The PV specifies no mountOptions.
Historical compaction reduced filesystem usage to about 207 GiB; the current
reading is about 214.7 GiB. These differently timed observations are not a stable
post-retention growth series. Collect daily readings for at least a week before
forecasting. At the *old assumed* 14 GiB/day, current headroom is about 53.5 days;
756.8 GiB / 14 GiB/day is about 54 days, never eight months.

## Accepted OpenBao development posture

The existing NovaForge development OpenBao keeps its root token and unseal key
in `novaforge-bao/openbao-init`. The user accepted that development convenience.
Production onboarding is separate: threshold unseal custody outside the cluster,
restricted recovery access, tested backup/restore, and scoped short-lived broker
credentials. The deployment fixture adds only a namespaced target role and its
own engine/policy; it does not re-key or change the shared development provider's
unseal arrangement.
