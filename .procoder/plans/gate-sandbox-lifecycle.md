# Namespace-scoped Gates execution lifecycle

## Scope and authority

Implementation contract for the operator-selected dedicated sandbox namespace
(see `.procoder/ask/decisions.md`). This is planned work, not implementation or
acceptance evidence. Activation remains blocked by the five findings in review
`66bd1cf3-c0b0-4540-bdff-ecdb24ab437b`.

Preserve authenticated organization scope, exact source/policy pairing, existing
cache writer fencing, proof provenance, bounded output and no local fallback.
Do not change the shared deployment while qualification is incomplete.

## Durable authority before effects

Add a Gates-owned invocation ledger and evaluation-attempt linkage, with no
cross-schema reads or cascading removal of unresolved obligations. A record binds:

- Invocation ID, evaluation-attempt ID, gate and tool identity.
- Authenticated org, run and repository; source SHA and policy SHA.
- Source snapshot digest, image digest and canonical command digest.
- Immutable configured execution target and namespace, predetermined unique pod
  name, and expected container set/identity. Pod UID becomes immutable on binding.

Persist digests/observable evidence, not source archives, bearer credentials or
model reasoning. Command identity must hash an unambiguous encoding of the actual
command/argv dispatched, not a human-readable concatenation.

Reserve outstanding-obligation capacity and persist intent atomically before any
Kubernetes call. A lost database acknowledgement requires exact readback of the
immutable identity before advancing. A separate durable single-owner dispatch
transition precedes Create; only its original acknowledged dispatcher may Create.
An uncertain transition cannot be used to elect another dispatcher or replay the
request. Recovery never creates pods or replays executable commands.

A Kubernetes timeout/cancellation/AlreadyExists response is not no-start evidence.
AlreadyExists can be observed only after validating the complete durable identity;
it does not authorize replay. An initial NotFound cannot settle uncertain Create,
since the original request may arrive later. Persist the returned/observed UID
before any source upload or command; uncertain UID persistence prevents exec.

Record separately: dispatch uncertainty, UID binding, executable-command dispatch,
observed terminal state, tool-result availability and resource deletion. Persist
terminal observation before Delete. Conflicting terminal receipt replay fails.
Unknown output or a dead pod must never synthesize a successful gate result.

## Result linkage and identity fences

An evaluation has its own attempt ID; every tool invocation links to it. Accepted
results retain their invocation references alongside exact source/policy identity.
Save/cache publication must refuse foreign, mismatched, unresolved or conflicting
evidence. Multiple tools and concurrent evaluations cannot share a mutable
execution slot. Recovery can settle lifecycle obligations, never publish late
passing evaluations when the original output is missing.

Generate a unique pod name per durable invocation and never reuse it. Before
every exec (mkdir, upload, command, report read and termination), GET and validate
namespace/name/UID, immutable identity and expected container set. Observe again
before accepting output. A mismatch or missing object stops exec and preserves
the obligation. Delete only with the recorded pod UID precondition after durable
terminal observation; never delete the shared namespace.

Kubernetes exec has no UID precondition: GET/exec is not atomic. The explicit
trust boundary is that only the trusted allocator creates sandbox pods and never
recreates an invocation name. Tenants and janitors cannot create replacement pods.
An operator with equivalent creation authority is outside this guarantee; do not
claim protection against that actor or atomic UID-bound exec.

## Provisioned namespace boundary

Require image and namespace together; both absent disables execution, partial
configuration fails closed. Reject invalid, default/system and application
namespaces. No caller/repository selection or fallback to application namespace.

Provision a dedicated Gates service account independently of generic service
RBAC. Reject `services.gates.rbac=true`. The execution Role is namespace-scoped:

- `pods`: create, get, delete.
- `pods/exec`: create (POST).

No namespace, policy, secret, log, token-request, wildcard or RBAC mutation access.
Pre-provision a credential-free namespace with an unprivileged sandbox service
account, all-pod ingress/egress denial, restricted admission and quotas. Gates
cannot alter those controls. Preserve the bundle across upgrades; enablement
cannot disable its protections. Do not colocate application resources or secrets.

NetworkPolicy allows are additive; qualify the effective policy composition and
CNI dataplane. Restricted Pod Security alone does not ban Secret volumes, token
projections or another service account. Either operator-owned admission enforces
those properties, or evidence must explicitly retain the trusted allocator and
credential-free-namespace assumptions. Do not add runtime permissions merely to
inspect provisioning; verification uses separately authorized operator identity.

## Capacity, recovery and deletion

Operator quotas must include `count/pods` plus CPU, memory and ephemeral-storage
bounds. Separately configure and atomically enforce a durable outstanding-
invocation ceiling, including uncertain creation, uncertain termination and
pending cleanup. Ceiling values are explicit operator configuration, not inferred
from pod deadlines or resource quota. Concurrent reservations serialize against
the same authority; new attempt IDs cannot evade the ceiling.

For conservative initial implementation, release reserved capacity only after
both durable terminal observation and confirmed resource absence following
UID-preconditioned cleanup. A missing pod before terminal evidence never releases
capacity. Unknown status, expiry, API-object removal and elapsed deadlines do not
prove execution termination. Retained obligations may deliberately block admission.

Enumerate unresolved invocation IDs from the Gates ledger, re-enter their org
scope, and observe exact persisted names. Startup and periodic recovery must be
wired into the real command. Observer/janitor credentials are separate and limited
to pod GET/DELETE in sandbox namespaces: no create, exec, secrets or tenant tokens.
No Kubernetes list/watch or arbitrary-name public cleanup API is needed.

Run/org deletion fences new admission under the same serialization used by
reservation, retains unresolved obligations and immutable evidence, and defers
purge until settlement. Do not depend on fresh membership or Git/Reviews access
for cleanup. A changing target configuration must not redirect existing records
to a different cluster or namespace; retain the original target's recovery mapping.

## Implementation and acceptance sequence

1. Ledger migration/store, bounded atomic admission, deletion fences and immutable
   evidence linkage. Real owned PostgreSQL tests precede executor changes.
2. Namespaced allocator using the journal; identity checks before all execs;
   terminal receipts before UID-preconditioned deletion. No namespace/policy calls.
3. Explicit paired configuration, dedicated service accounts/RoleBindings,
   admission/network/quota bundle; independent chart and authority review.
4. Separately scoped observer/janitor and startup recovery; preserve uncertain
   create/exec/delete obligations across actual process restart.
5. Qualified image and owned real-cluster fixtures; retain all four existing
   merge/project-policy/approval assertions without skips or permissive executors.

Required negative/crash tests include:

- Committed-but-unacknowledged intent/UID updates; accepted-but-unacknowledged or
  delayed Create, including NotFound followed by late creation; no duplicate dispatch.
- Crash before UID persistence, during exec, after terminal persistence and after
  Delete; concurrent recovery and run/org deletion racing admission.
- Mutation of every bound identity field; several tools/evaluations; stale source
  or policy; injected/wrong containers; conflicting receipts; unavailable output.
- Replacement around readiness/upload/command/report/termination and Delete UID
  conflict. A transport race test must document the non-atomic GET/exec limitation.
- Unknown invocations filling capacity after their API objects disappear;
  concurrent reservations, quota rejection and terminal-but-undeleted resources.
- Unsafe/partial namespace configuration, broad-RBAC rejection, exact rendered
  accounts/roles, real forbidden API calls and hostile pod admission.
- Owned reachable canaries with positive controls, token/secret absence, report
  bounds, cancellation/descendants, and separately observed eventual pod deletion.

The existing hostile fixture's unrelated endpoints are not authorized probes.
Host fixtures, fake clients and configuration tests do not qualify Kubernetes
isolation, image advisory data, running-command startup or canonical completion.
