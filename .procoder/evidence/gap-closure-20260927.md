# September 27 gap-closure release evidence

Core release qualification is complete. The final sections record all-suite
acceptance, the expired-issuer rerun, cleanup and explicit remaining follow-ups.

## Identities and baseline

- Initial local main: f314f75, 38 commits ahead after refreshing origin/main.
- Initial live application: dc5f70f, Helm revision 9, 12 ready pods, zero restarts.
- Qualified application implementation: ba12eff, built on kw BuildKit and deployed
  through the normal Helm wrapper. Subsequent operator-runner images are pinned
  separately; docs, fixture and formatting commits do not change these app images.
- Baseline: 1,603 Go tests passed, three datastore connection failures passed on
  targeted rerun, four explicitly skipped. Final full regression: 46 tested
  packages passed, no failures. The owned-database UID fault was subsequently
  executed successfully in its disposable namespace.
- Remaining optional tests: standalone OpenBao/Postgres disposable-binary setup,
  live swarm-model credentials and interactive GUI opt-in. Actual cluster agent
  and browser evidence is recorded separately; skips are not counted as passes.

## Core functionality evidence

- Cross-fork executable gates fail broken source, pass corrected source and retain
  parent policy despite fork edits. The deployed merge suite passes independent
  approval and main-to-main cross-fork merge. The actual browser displays fork
  identity and the correct changed files/content.
- Signed push, Run and aggregate CI completion webhooks pass in git_host. Local
  real Redis/Postgres outage and restart tests preserve durable publication and
  bounded delivery retries. Runner terminal receipts now retry the exact receipt
  while the final log becomes durable, fixing the observed completion race.
- Standard SSH git-lfs transfers a random 2 MiB payload over verified HTTPS in
  git_host. Local authorization tests cover expired and mis-scoped tickets.
  The cluster refuses archived uploads and verifies fork payload survival,
  same-name UUID reuse and physical cleanup after repository/organization deletion.
- Real MinIO disconnection and restarted worker tests preserve cleanup intent;
  locked upload rows cannot be collected. Historical orphan auditing defaults to
  bounded, organization-scoped report-only mode with a minimum 24-hour grace.
- Exact application/Cilium outbound policies permit approved GitHub and controlled
  upstream imports/refresh/webhooks; forbidden destinations remain refused.
  Actual GUI checks cover explicit policy errors, mirror creation, refresh,
  writable conversion and fork creation. Import fidelity is Git refs/history.
- Actual cert-manager renewal changed certificate serial
  1D9022CA58808086A5B62EBBA2C43DE8 to 5206B439A54A06604EE94A1291DC212B
  with the same pod UID/restart count. Verified Git works before and after;
  plaintext remains refused. The rotation fixture was removed.

## Governed deployment qualification

The service now has production gates/REST/GUI callers and an OpenBao Kubernetes
credential path. Real signed 600-second service-account credentials were accepted
by the target and then refused as Unauthorized after expiry. Revocation removes
the generated service account and role bindings. Configuration is fixed by the
operator to org/repo, chart/image, cluster/namespace/release and target revision.

Testing found and corrected containerd image-status interpretation and Helm
failure evidence handling. Image identity remains checked against Pod spec and
runtime ImageID. Failed Helm descriptions are not stable provenance; durable
release labels must be read through revision-matched storage selectors. Earlier
ambiguous disposable operations remain uncertain in audit history, with resolved
credentials, rather than being retroactively declared successful. Their target
namespaces will be removed after qualification. Actual GUI reconciliation of the
first successful workload recovered its observed state and credential cleanup.

## Broader scope and operational decisions

The intelligence audit gives all eight candidate areas source evidence, bounded
follow-up or an explicit scope decision. The production-qualification follow-up
remains open: language/edge coverage, later-run knowledge recall, production MCP
credential routing, offline workspaces, model-specific pricing and advisory
coverage. Structural acceptance coverage does not claim those are finished.

The exact Nexus PVC trim proposal and suspended CronJob passed server dry-run;
they were not applied. Same-day measurements increased used storage by roughly
0.525 GiB, insufficient for a reliable growth forecast. Accepted development
OpenBao custody is unchanged; production custody remains a separate decision.

Detailed execution logs are retained locally under
/tmp/novaforge-gap-20260927/ and /tmp/e2e.*.log. This checked-in record preserves
results and limitations without credentials or raw deployment manifests.

## Qualified deployment executor

- Source: c9b6778.
- Runner: `192.168.10.131/novaforge/deployment-runner@sha256:8b9429c0f433e3cf09905bd3950a37983ee8a040b9205acce02056f35663ac6e`.
- Embedded chart SHA-256: `86fb4114694d85bcbbcc51b182a12e3f1b43f6a1fab2cae8dc06728158737ffe`.
- Controlled release: approved-app-final in novaforge-deploy-target.
- Targeted governed_deploy passed real workload success, independent approval,
  self-approval refusal, denial, changed-intent refusal, replay without execution,
  credential cleanup, revoked target write permission producing failed evidence,
  explicit retry after permission repair and attempt-2 observed graph provenance.
- Browser review found the approval dialog omitted deployment intent. The REST
  representation now exposes only the public bound-intent fields, and the dialog
  renders the exact artifact/destination/revision/request before decision.
  Application source 19b29cc includes this correction; final browser qualification
  is pending its rollout.
- Final changed-package run: edge 81.020s, deployment 104.079s, spectrace 2.257s,
  all passed. A serialization test refuses unrelated raw approval detail leakage.

Frontend qualification: all 32 existing ego-browser regression cases passed in
the same TaskSpace used for actual cluster GUI checks. TypeScript build/types,
final Go build/vet and procoder check passed; the latter reported zero blocking
findings. No additional browser or mock backend was introduced.

Final application rollout: 19b29cc, Helm revision 16; all 12 pods ready with
zero restarts. Exact runtime image identities are preserved in
`gap-closure-20260927-images.json`. Full 16-suite cluster acceptance is running.

Actual browser success on 19b29cc: request
`f7a40bff-278f-4fd0-ad43-fbd7953bb015`, independent reviewer approval after the
exact immutable intent was displayed, then author execution with attempt 1
succeeded. Stale prior request `9ac7397b-3442-471b-9ed9-e5c31ff048fa` was correctly
refused after a newer destination operation. Local browser transcript is retained.

The first full cluster run failed the secrets suite because its existing
NovaForge development CI issuer expired September 26 at 13:14:28 UTC. It was
renewed with the exact-scope fixture tool; the new CA expires October 27 at
11:10:31 UTC, with unchanged 600-second default/3600-second maximum leaf policy.
The failed issuance's reservation `c20b6e47-1ac1-4236-b62c-e14621264566` remains
`issuance_unknown`, as no provider lease handle was returned. No credential
reached the job. An explicit open operator-reconciliation task records this
limitation; renewing the issuer is not evidence to erase the obligation.
Both agent and agent_ci passed in the final run; the historical gateway failure
did not recur.

## Final cluster run and restored installation

At application 19b29cc / Helm revision 16, the full harness passed 15 suites:
airgap, deploy, work_ci, gui, search, graph, factory, agent, agent_ci, merge, cli,
crossorg, git_host, cert_rotation and governed_deploy. Secrets failed on the
expired development issuer described above; its rerun passed under restored
Helm revision 17 after issuer renewal. The agent suite
ended succeeded. Agent-CI passed its terminal-outcome propagation criterion with
an agent/job failure; that is not a claim of a successful model review. No gateway
timeout was observed in the runtime log inspection.

Final rotation changed serial B4E47592A9BBCCA44B423378F90BAD79 to
1AB5C01DBBCB2B810B3BD28AB684810C with unchanged pod UID/restart count and verified
Git before and after. Final governed deployment again passed actual failure/retry,
cleanup and observed graph projection on the qualified runner digest.

Helm revision 17 restores the original novaforge-openbao broker Secret and disables
the disposable deployment target. The temporary outbound fixture rule is removed;
only the explicit GitHub HTTPS connected rule remains. Application images remain
19b29cc. All deployment-fixture credential obligations were resolved before
cleanup. The fixture organization, provider mount/policy/token and operator
Secrets and namespaces were removed. Restored secrets and readiness acceptance
passed, confirming the original broker configuration works.
The fixture browser session was signed out.

Restored acceptance completed: secrets PASS, deploy PASS, airgap PASS at Helm
revision 17 with unchanged application images. The complete 16-suite evidence
therefore consists of the 15 passing full-run suites plus the corrected secrets
rerun; the initial issuer failure remains documented. All 12 restored pods are
ready with zero restarts. The three fixture namespaces and three operator Secrets
are absent. Seven owned build worktrees, local registry/BuildKit tunnels and the
temporary secret-bearing Helm values copy were removed; user configuration and
non-secret execution evidence remain intact.
