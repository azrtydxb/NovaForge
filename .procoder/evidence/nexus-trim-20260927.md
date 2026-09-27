# Nexus trim qualification — September 27

The user's “ok do the follow ups” authorized qualification and activation of the
previous exact-PVC proposal. Applied suspended, then created Job
`nexus-trim-qualification-20260927` from `nexus-volume-trim` in cluster kw,
namespace nexus. The Job completed successfully with exit 0, no restarts, using
the pinned Alpine digest and bounded capabilities in the checked-in manifest.

Target: PVC `nexus-data-iscsi`, PV `pvc-1a9dcde3-a41e-49af-bdd1-05fcb922440f`,
1 TiB ext4 RWO, co-located with Nexus on master-12. No host mount or API token.
Command output: `/nexus-data: 10810601472 bytes trimmed` (10.07 GiB).

Read-only ZFS observations on the actual iSCSI portal 192.168.10.253, dataset
`Pool0/k8s/pvc-1a9dcde3-a41e-49af-bdd1-05fcb922440f`:

| Property (bytes)                  |        Before |         After |
| --------------------------------- | ------------: | ------------: |
| used / referenced / usedbydataset |  236434479840 |  231196146480 |
| logicalused                       |  239131041792 |  233872650240 |
| volsize                           | 1099511627776 | 1099511627776 |

Backing allocation decreased **5,238,333,360 bytes (4.88 GiB)** during the
qualification interval. This measured decrease differs from the command's
reported discard count; concurrent Nexus writes prevent exact attribution of
every byte. It establishes recovered allocation, not a long-term growth forecast.

Nexus pod `nexus-5645c89dfb-z6dqx` remained 2/2 Ready, zero restarts.
Post-job local `/service/rest/v1/status` returned HTTP 200 in 0.002 seconds.
No continuous request-latency trace was collected during the short trim itself;
this does not establish a latency percentile or absence of every transient pause.

Activated the checked-in CronJob after these checks: `suspend=false`, Sunday
00:00 UTC / 04:00 Dubai, Forbid concurrency, 300-second deadline. The workload's
mount options and Helm resources were unchanged. Job logs remain in Kubernetes
with a seven-day TTL. Local raw observations are under
`/tmp/novaforge-followups-20260927/nexus-{zfs,df}-{before,after}.*`.

Rollback: `kubectl --context kw -n nexus patch cronjob nexus-volume-trim --type merge -p '{"spec":{"suspend":true}}'`.
Delete an active Job if required; delete the CronJob to remove the schedule.
A completed discard cannot be reversed and affects filesystem-free blocks only.
