# Intelligence follow-up qualification — September 27, 2026

Application image `c8ade67`, built on kw and deployed through `hack/deploy.sh`.
Helm revision 18 exercised the temporary qualification configuration. Source
`3f99855` additionally contains the semantic image fixes and dedicated chart RBAC.
The evidence directory `intelligence-20260927/` contains public observations,
not credentials, private keys or model chain-of-thought.

## Semantic production

The producer image is pinned to
`sha256:c5fd41778d2a0e755e211a68af6a832086c946659d67b7b0bb084e71bc301ac5`
(source `1a53c49`); execution digest
`64f9b4448784324d439fad8a9eb0233e091f5d67e05742e2f9b5cd0168151c50`.
The retained config specifies arm64, 1000m CPU, 2048 MiB memory/storage and 300s.
Real isolated producer jobs found cross-file SCIP definitions/references for Go,
TypeScript and Python; Go also returned LSP definitions. Default-branch pushes
then produced semantic symbols and caller-file edges through the deployed graph
API. Feature-branch changes did not overwrite the default branch. Unsupported
Java produced no semantic evidence; syntactically broken Go failed explicitly.
See `docs/semantic-production-coverage.md` for edge and incompleteness limits.

The live work exposed and fixed two image-build/runtime seams (SCIP's local Go
module replacements require its complete pinned source tree; Python needs pip
metadata) and missing controller RBAC. The chart regression was observed red
before the dedicated role was added, then passed.

## Knowledge in a later run

The production API recorded decision
`ab8123e5-6972-4ca9-8b15-8a32b3014c03` at `2026-09-27T12:15:58Z` with marker
`NF-VAT-20260927`. Later actual run
`9c0fc93d-37fe-4473-a00b-ac341b5f8282` had no tools available and returned the
exact entry ID, timestamp, marker and person-recorded provenance from its opening
context. Production-service context assembly included it for related work,
excluded it for unrelated work, and excluded the matching foreign-organization
control decision. Retained context digests distinguish those observations.

This exposed missing provenance in the assembled brief. The provenance regression
failed before the fix and passed afterward; title/body alone is no longer the
only information carried into the run.

## MCP production wiring and confinement

Operator policy binds organization, repository, approved server identity,
transport and destination. Credentials are mounted references read per request;
stdio argv is operator-owned and executed through Kubernetes inside the run's
isolated workspace. Source wiring assertions connect those paths to production.

Run `18efb166-fd1a-40df-a2aa-9b7472cea0da` successfully called both the actual
NovaForge HTTP MCP server through a temporary verified TLS proxy and a functional
Python stdio server that read the workspace note and computed its hash. The first
attempt used a UUID where a work-item key was required: HTTP reached the service
but the tool failed. Only the corrected retry proves both successful tools.

A separate real-cluster integration harness used the production registry RPC,
operator loader, MCP client and audit store. On the same open HTTP session, an
invalid replacement credential and a removed credential refused calls; restoring
the original credential restored access. Destination and organization substitutions
were refused. Revocation through the actual GUI then refused the next call on
that existing session. These credential changes affected the harness's private
copy of mounted configuration, not a model-supplied credential or fake server.

Cancellation run `5dfe2746-ffba-4657-b132-aa56e727eb54` was cancelled after the
Python MCP process was observed in its workspace pod. The initial API response
reported cleanup pending; later authenticated GetRun confirms execution finished
and all grant/work/workspace cleanup flags false, with the namespace absent.
The real workspace isolation regression also checks detached-child termination,
API egress denial and absence of service-account tokens. No MCP command is
executed on the runtime host. Literal credential scans found no fixture credential
in agent-runtime, MCP-server or edge logs during the observed 90-minute window;
this is not a guarantee about arbitrary encodings or future logs.

## Offline dependencies and model budgets

`TestVendoredDependencyBuildInIsolatedWorkspace` built and ran a real program
using vendored `github.com/google/uuid` v1.6.0 in a deny-egress workspace. The
external control destination was reachable outside the pod and blocked inside it.
The operator-cache/vendor recipe is `docs/offline-dependencies.md`.

Run `6e543adf-c7d3-42e1-8a4a-745cd378a8ff` selected its model in repository
configuration and stopped `over_budget` at 1,005 tokens, below its 50,000-token
limit and 240-second clock. Synthetic qualification prices (not real billing
rates) made the cost 1,005,000,000 micros against a 1-micro limit. A separately
selected unpriced model failed before spending tokens in run
`2fe7dedf-e198-4eea-b1e2-8f53d49a3e48`. Overflow and effective-model lookup
regressions pass. Original operator pricing is restored after qualification.

## Offline advisory matrix

Real isolated workspaces ran the production vulnerability scanner against image
`sha256:18c5135159600a81a9aa66d2bdc19ceea08c7be762e46c271855f67d12dc3116`.
Concrete Go, npm, PyPI and crates.io dependencies each produced vulnerability
findings. Missing, expired and corrupted snapshots failed closed. Negative
fixtures modified only disposable test-container files, not the published image.
The retained manifest was generated September 27 at 12:26:51 UTC and expires
December 26 at 12:26:51 UTC. `docs/offline-advisories.md` specifies qualified
inputs, limits and the cache-invalidating image refresh procedure.

## Regression and operational boundaries

All six targeted in-cluster suites passed: airgap, gui, graph, crossorg, agent
and agent_ci. The agent and agent-backed CI job both succeeded. Browser evidence
covers MCP approval/revocation, agent history and the recorded knowledge decision.

The full configured Go run passed 45 tested packages and failed one agents replay
test that expected four duplicate notifications but saw two while packages shared
the dev publisher. The isolated replay test and then the whole agents package
passed. Thus all 46 tested packages have passing evidence, but the initial full
run was not entirely green. Twenty-two packages have no tests. Some opt-in cases
were skipped (GUI fixture, OpenBao PostgreSQL credential, configured-model swarm
decomposition, and namespaced sandbox UID-commit failure); this report does not
claim those cases ran. Go build/vet and the final Helm service-chart tests pass.

The 16-suite core release evidence remains historical evidence for `19b29cc`;
this follow-up does not claim all 16 suites were repeated on `c8ade67`.
Provider reconciliation is separate and remains open because independent evidence
for the historical unknown private-key issuance is absent. Nexus weekly trim is
qualified and active as documented in `nexus-trim-20260927.md`.

## Final deployment and retained harnesses

Helm revision **19** restores original operator model prices and removes the
fixture MCP configuration while retaining the qualified semantic producer.
Application image remains `c8ade67`; all 12 pods are ready with zero restarts and
both public load-balancer checks pass. The disposable organizations, operator
MCP Secret and TLS namespace were deleted through their owning APIs.

The `*-harness.go.txt` files retain the exact Go harness source, formatted for
review. They import this repository's production packages and use real cluster
and service clients. These are historical qualification harnesses, not a new
supported CLI: fixture.json, private operator configuration and local forwarding
addresses were supplied only for this execution and are not committed. Reproduce
with new disposable organizations and repositories, explicit operator bindings,
the pinned images/configuration and the production APIs described above. Never
reuse the now-deleted fixture credentials. Existing repeatable package tests and
`hack/e2e-in-cluster.sh airgap gui graph crossorg agent agent_ci` cover the
maintained regressions.
