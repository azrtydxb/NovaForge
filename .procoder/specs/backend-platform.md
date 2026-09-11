# backend-platform

Status: complete

## Problem

Git hosting platforms assume a human author behind every commit. When AI agents are bolted
on — as a chat box beside the diff — the platform has no way to constrain what the agent may
touch, no isolation for what it runs, no record of why it concluded what it did, and no
verification the agent cannot talk its way past. The agent is handed a broad personal access
token and trusted. That is unacceptable for teams that want agents doing real engineering
work on real repositories, and it is why agent-authored changes today get reviewed
line-by-line by humans, which erases the leverage they were meant to provide.

NovaForge's backend is the control plane that fixes this: Git stays fully standard, but
permissions, execution isolation, context assembly, verification, and merge authority live in
the platform rather than in the model. This spec covers that entire backend across all six
phases of section 26. The web GUI is deferred to a later spec — the backend must be complete
and driveable without it.

## Users

- **Human engineers** — clone, push, review, and merge over standard Git (SSH and HTTPS) with
  no NovaForge-specific client. They need the platform to be unremarkable as a Git host, and
  to spend review attention on exceptions (section 21) rather than every generated line.
- **AI agents** — first-class contributors with identities, roles, and histories (section 6).
  They need scoped capabilities, an isolated workspace, typed tools (section 13), and
  assembled context (section 17) — not a shell and a token.
- **Platform/API clients** — the deferred GUI, the nf CLI, and external agents such as Claude
  Code and Codex driving NovaForge through its own MCP server (section 15). All three consume
  the same OpenAPI contract, so the API must be complete with no GUI to hide gaps behind.
- **Operators** — self-hosted on Kubernetes, including air-gapped sites (section 1). They
  need the whole stack to run with no egress to any hosted model provider.
- **Runners** — execution workers that receive deterministic and agent jobs (section 19) and
  stream logs and artifacts back.

## In scope

Backend only. Service decomposition follows the section 2 architecture diagram: identity,
git-platform, work-reviews, ci-runner, agent-runtime, gates, engineering-graph, and
mcp-server.

- [S-1] **Identity and access**: users, organizations, teams, sessions, personal access
  tokens, TOTP two-factor, SSH public keys.
- [S-2] **Git platform**: bare repository storage, smart-HTTP transport, embedded SSH server,
  branches, tags, commits, diffs, tree and blob browsing.
- [S-3] **Capability-based authorization** (section 7): scoped per-run capability grants
  enforced by the platform — never a broad token issued to an agent.
- [S-4] **Work Items** (section 5): typed engineering intent carrying goal, acceptance
  criteria, constraints, and required gates; assignable to humans or agents.
- [S-5] **Engineering Runs** (section 9): pull requests carrying plan, change impact, and
  proof; reviews, comments, and independent multi-agent review (section 11).
- [S-6] **CI and runners** (section 19): workflow definitions, job scheduling, runner
  registration, live log streaming, artifact storage, and agent jobs as first-class CI jobs.
- [S-7] **Agent runtime** (section 8): ephemeral isolated workspaces per Agent Run, lifecycle
  management, event streaming, and provenance capture (section 6).
- [S-8] **Typed agent tools** (section 13): the audited tool surface agents call instead of
  shell access.
- [S-9] **go-ai-sdk integration** (section 4): all model access through go-ai-sdk, with no
  provider-specific AI logic anywhere in NovaForge.
- [S-10] **Gate controller** (section 10): post-completion enforcement of tests,
  architecture, security, API-compatibility, dependency, quality, and documentation gates.
  The gate set and its YAML shape are defined in `NovaForge_AI_Native_Git_Platform.md`
  section 10; enforcement is structurally impossible for the agent to bypass.
- [S-11] **Approval model** (section 14): policy-controlled approvals mapped onto go-ai-sdk
  approvals, with the model never deciding its own permissions.
- [S-12] **Secret brokering** (section 20): short-lived scoped credentials issued per
  policy instead of durable secrets, following the brokering flow in
  `NovaForge_AI_Native_Git_Platform.md` section 20.
- [S-13] **MCP server** (section 15): NovaForge's own MCP surface so external agents can drive
  it, plus a client for approved external MCP servers.
- [S-14] **Engineering Graph** (section 16): relationships across symbols, services, APIs,
  schemas, tests, Work Items, commits, ADRs, deployments, owners, and incidents.
- [S-15] **Code intelligence and indexing**: Tree-sitter, LSP, and SCIP symbol and dependency
  indexing feeding the graph.
- [S-16] **Context assembly** (section 17): per-Work-Item retrieval across lexical, symbol,
  dependency, semantic, history, and test signals with reranking — never whole-repository
  dumps.
- [S-17] **Persistent project knowledge** (section 18): project-owned memory of decisions,
  patterns, incidents, and human corrections, injected into later agent context.
- [S-18] **Repository-level configuration** (section 12): the .novaforge directory under
  source control, holding project config, agent definitions, gate definitions, context
  documents, and MCP server declarations.
- [S-19] **Agent swarms** (section 22): Work Item decomposition into dependency-ordered
  subtasks across specialized agents.
- [S-20] **Autonomous maintenance** (section 23): detection of the eight conditions listed
  in `NovaForge_AI_Native_Git_Platform.md` section 23 — outdated dependencies, CVEs, flaky
  tests, dead code, coverage regression, documentation drift, performance regression, and
  architectural violations — each proposing a Work Item for approval.
- [S-21] **OpenAPI contract and nf CLI**: the complete REST edge plus a CLI that exercises it,
  standing in for the absent GUI.
- [S-22] **Kubernetes deployment**: Helm charts deploying every service, with agent runs
  executing as pods in per-run namespaces.

## Out of scope

- **The web GUI** — deferred to its own spec. No React, Vite, TanStack, Shadcn, or Monaco work
  here. The backend must be fully driveable through OpenAPI, the nf CLI, and MCP without it.
- **GitHub/GitLab mirroring and import** — treated as part of the SCM integrations section 25
  defers.
- Everything else section 25 defers: large wiki systems, portfolio management, full Kubernetes
  management as a product feature, observability suites, enterprise project management.
- Building model inference — FastLLM, vLLM, and DGX Spark are integrated, not implemented.
- Building go-ai-sdk or ProCoder — both are consumed as dependencies.
- Git LFS beyond what standard Git transport provides.
- Firecracker isolation — section 8 lists it as an eventual step; Kubernetes pods are the
  isolation boundary for this build.
- docker-compose deployment — Kubernetes and Helm are the only supported deployment path.

## Constraints

- **Go backend, microservices.** This deliberately reverses section 3's instruction to use a
  modular monolith initially. The design document is left unchanged; this spec is the newer
  decision. Rationale: the agent runtime, runners, and indexing have resource profiles
  unrelated to the API and must scale independently; an agent-runtime or indexing failure must
  not take Git hosting down, since Git transport availability is the platform's floor; the
  agent and gate layers need to ship on a faster cadence than identity and Git; and
  agent-facing services run under different trust and network policy than identity, which
  process and namespace isolation enforces.
- **Standard Git compatibility is absolute** (section 1): clone, fetch, pull, push, SSH,
  HTTPS, branches, and tags must work with an unmodified git client.
- **Git is implemented by shelling out to the git binary**, not a pure-Go library —
  compatibility outranks purity. Every image touching repositories must contain git, and its
  version is asserted at startup.
- **SSH is served by an embedded Go SSH server** built on golang.org/x/crypto/ssh,
  authenticating against stored public keys. No host sshd dependency.
- **Internal transport is gRPC; the client edge is REST described by OpenAPI; events are Redis
  Streams.** Redis is the event bus per section 3's "Redis initially"; NATS is a later
  migration, not a dependency of this build.
- **One PostgreSQL cluster, one schema per service.** No service reads another service's
  tables; cross-service reads go through that service's gRPC API.
- **Organizations are a hard security boundary.** Multi-tenancy is required: per-org
  namespaces, per-org credentials, and no data path between orgs. Every authorization check is
  org-scoped, and no query may be satisfiable without an org predicate.
- **Air-gapped operation is a first-class deployment model** (section 1). No feature may
  hard-depend on reaching a hosted model provider or the public internet.
- **No provider-specific AI logic in NovaForge** (section 4) — model access is exclusively
  through go-ai-sdk.
- **Agents cannot bypass gates** (section 10) — enforcement is structural, not convention.
- **Evidence, not chain-of-thought** (section 6) — only observable actions and artifacts are
  stored.
- **Enterprise scale targets**: approximately 10,000 repositories, 5,000 users, 200 concurrent
  agent runs, and monorepos up to 50 GB. Storage, indexing, and context assembly are designed
  against these numbers rather than retrofitted.
- **Platform floors**: Go 1.26 or later, PostgreSQL 16 or later with pgvector, Redis 7 or
  later with Streams and consumer groups, Kubernetes 1.29 or later.
- **Redis durability**: AOF persistence with consumer-group redelivery gives at-least-once
  delivery across a restart. Every stream handler must therefore be idempotent; exactly-once
  is not assumed anywhere.

## Interfaces

- **Git transports**: smart-HTTP under a repository path ending in .git, exposing info/refs,
  git-upload-pack, and git-receive-pack; plus SSH on the embedded server.
- **REST edge**: the complete API, contract-first in an OpenAPI document at api/openapi.yaml.
  Server-sent events carry live agent and CI event streams; WebSockets are used only for
  genuinely interactive sessions, per section 3.
- **gRPC**: internal service-to-service contracts, one protobuf package per service.
- **nf CLI**: the human and automation entry point while the GUI is deferred.
- **MCP server** (section 15): conforms to the current MCP specification revision only, with
  no legacy compatibility — stdio and Streamable HTTP transports, explicitly excluding the
  deprecated HTTP+SSE transport. It exposes the operations section 15 lists: fetching a Work
  Item, searching a repository, resolving a symbol, creating a branch, reading a review,
  running CI, and reading gate status.
- **Typed agent tools** (section 13): the thirteen tools section 13 enumerates, covering
  repository search, file reads, symbol and dependency lookup, workspace writes, diff and
  commit, test execution and log retrieval, Work Item read and comment, architecture queries,
  and gate status.
- **Repository configuration** (section 12): the .novaforge directory holds a project
  configuration file, an agents directory, a gates directory, a context directory of Markdown,
  and an MCP directory. All are YAML except context documents.
- **Work Item and gate configuration formats** follow the YAML shapes given in sections 5 and 10.
- **Runner protocol**: runners open a persistent outbound gRPC stream and the platform pushes
  jobs down it. Runners never need inbound network reachability.
- **ProCoder**: invoked as a binary by the gate controller inside the job image; its exit
  status and structured output are parsed into gate results.

## Data

Owned per service, one PostgreSQL schema each, in a shared cluster. Every table carrying
tenant data is org-scoped.

- **Identity**: users, organizations, teams, memberships, sessions, personal access tokens,
  TOTP secrets, SSH public keys.
- **Git**: repository, branch, tag, and commit metadata. Object data is the bare repository on
  disk; PostgreSQL holds metadata only.
- **Work**: Work Items with type, goal, acceptance criteria, constraints, required gates, and
  assignment to a human or agent.
- **Reviews**: engineering runs, plans, change-impact records, proof records, reviews, and
  comments.
- **CI**: workflow definitions, runs, jobs, runner registrations, and artifact metadata.
- **Agents**: agent identities, roles, runs, provenance records, and the tool-call audit log.
- **Gates**: gate definitions, evaluations, and outcomes.
- **Graph and knowledge**: symbols, dependencies, relationships, pgvector embeddings, and
  project knowledge entries.

Storage outside PostgreSQL:

- **Bare repositories** live on a shared network filesystem mounted read-write-many, so any
  git-platform replica can serve any repository.
- **CI artifacts and sealed logs** live in self-hosted S3-compatible object storage (MinIO),
  which works air-gapped and keeps the S3 API available if real S3 is ever used.
- **Live log tails** stream through Redis and are sealed into object storage when the job
  ends.

Retention is configurable per organization. Defaults: provenance records, gate outcomes, and
the tool-call audit log are kept indefinitely because they justify merge decisions; raw CI and
agent logs expire after 90 days.

## Edge cases

- A push updating many refs at once, or a force-push rewriting history that a Work Item or
  Engineering Run already references.
- An agent scoped to a single branch namespace attempting to write any other ref — whether
  through Git transport or through a commit tool call. Both paths must refuse identically.
- Two agents assigned overlapping Work Items touching the same files; swarm subtasks whose
  dependency ordering is violated by an upstream failure.
- An agent declaring completion with required gates never run, or editing gate definitions
  inside the .novaforge directory in the same change it is trying to merge.
- A repository with no commits; a 50 GB monorepo; a binary-heavy repository; a repository
  whose .novaforge configuration is malformed or absent.
- A clone arriving while a receive-pack for the same repository is mid-flight.
- An Agent Run's pod destroyed before evidence is persisted.
- Context assembly running while the index is stale, missing, or mid-rebuild.
- An agent run reaching its wall-clock, token, or cost limit mid-tool-call.
- A human pushing to an agent branch while a run holds it.
- Any request whose org predicate is absent or mismatched, which must be refused rather than
  silently widened.

## Failure modes

- **PostgreSQL down**: the platform is unavailable for writes; Git transport must refuse a
  push rather than accept one it cannot record.
- **Redis down**: event delivery stops and scheduling stalls. On restart, AOF plus
  consumer-group redelivery replays in-flight messages at least once, so handlers must be
  idempotent.
- **A service down**: gRPC callers degrade rather than cascade. A gate controller that cannot
  be reached means merge is blocked, never permitted — every gate path fails closed.
- **Model gateway down or slow**: agent jobs fail or queue; deterministic CI and all Git
  operations continue unaffected.
- **Runner dies mid-job**: the job is detectable as orphaned through the broken gRPC stream,
  then rescheduled or failed — never left running indefinitely.
- **Agent workspace leaks**: per-run namespaces are reaped by a reconciler even when the
  controlling service crashed.
- **git binary missing or version-mismatched**: the service fails at startup rather than at
  first push.
- **Corrupt repository on disk, or the shared filesystem full** during receive-pack.
- **Shared filesystem unavailable**: git-platform reports unhealthy and stops accepting
  transport rather than serving partial reads.
- **External MCP server** unreachable, slow, or returning hostile output — treated as
  untrusted input and never as instructions.
- **Secret broker unavailable**: fails closed. Jobs requiring credentials block; jobs
  requiring none continue to run.
- **Object storage unavailable**: running jobs continue and buffer, but artifact upload
  failure fails the job rather than silently dropping evidence.

## Acceptance criteria

- [ ] [S-1] `TestRegisterLoginTOTP`: a user registers, logs in, enables TOTP, and a subsequent
      login without a valid TOTP code is rejected.
- [ ] [S-1] `TestPATGitClone`: a personal access token authenticates a Git-over-HTTPS clone, and
      a revoked token is rejected.
- [ ] [S-2] `TestStandardGitRoundTrip`: an unmodified git client clones, commits, and pushes
      over both HTTPS and SSH, and the pushed commits are readable through the REST API.
- [ ] [S-2] `TestRepoBrowseAPI`: branches, tags, commit history, diffs, and tree and blob reads
      are retrievable through the REST API for a repository pushed by a standard client.
- [ ] [S-3] `TestAgentBranchScopeEnforced`: an agent granted write access to one branch
      namespace is refused a push to the default branch, identically through Git transport and
      through the commit tool.
- [ ] [S-3] `TestCrossOrgAccessDenied`: a request carrying one org's credentials cannot read or
      write any other org's repositories, Work Items, or runs.
- [ ] [S-4] `TestWorkItemLifecycle`: a Work Item is created with type, goal, acceptance
      criteria, constraints, and required gates, and is assignable to either a human or an
      agent.
- [ ] [S-5] `TestEngineeringRunProof`: an Engineering Run exposes plan, change impact, and
      per-gate proof, and records which agent and model produced the change.
- [ ] [S-5] `TestSelfApprovalRejected`: a change authored by one agent cannot be approved solely
      by that same agent.
- [ ] [S-6] `TestRunnerJobStreamAndArtifact`: a runner receives a job over its gRPC stream,
      streams logs observable while the job runs, and uploads an artifact retrievable
      afterwards.
- [ ] [S-6] `TestAgentCIJob`: a CI job declared with an agent role executes as an agent job and
      reports status like any other job.
- [ ] [S-7] `TestAgentRunIsolationAndEvidence`: an Agent Run executes in its own Kubernetes
      namespace, and Git changes, events, and evidence persist after the namespace is
      destroyed.
- [ ] [S-7] `TestRunBudgetHardStop`: a run exceeding its wall-clock, token, or cost limit is
      terminated, marked failed-over-budget, and retains its evidence.
- [ ] [S-7] `TestAgentBranchLockedDuringRun`: a human push to an agent branch is rejected while
      a run holds it.
- [ ] [S-8] `TestToolCallAudited`: every tool call an agent makes is recorded with its
      arguments and outcome in the audit log.
- [ ] [S-9] `TestAirGappedAgentRun`: an agent run completes against a local FastLLM-served model
      with no network egress to any hosted provider.
- [ ] [S-10] `TestGateBlocksMerge`: an agent declaring completion with a failing required gate
      cannot merge.
- [ ] [S-10] `TestGateConfigSelfEditRejected`: the same refusal holds when the change under
      review edits its own gate definitions.
- [ ] [S-10] `TestGateControllerUnreachableBlocks`: with the gate controller unreachable, merge
      is blocked rather than permitted.
- [ ] [S-11] `TestApprovalPaths`: adding a dependency, changing a database schema, and deploying
      to production each follow their section 14 approval path, and no model output can alter
      its own approval requirement.
- [ ] [S-12] `TestShortLivedCredential`: a job receives a credential that expires on schedule,
      and a staging-scoped run requesting a production credential is denied.
- [ ] [S-12] `TestBrokerDownFailsClosed`: with the secret broker unavailable, credential-needing
      jobs block while credential-free jobs still run.
- [ ] [S-13] `TestMCPServerOperations`: an external MCP client retrieves a Work Item, searches a
      repository, creates a branch, and reads gate status over both stdio and Streamable HTTP.
- [ ] [S-14] `TestGraphQueries`: the graph answers, for a given symbol, which services depend on
      it, which tests cover it, and which Work Item last changed it.
- [ ] [S-15] `TestIndexUpdatedOnPush`: pushing a commit updates the symbol and dependency index
      for the changed files.
- [ ] [S-16] `TestContextBounded`: context assembled for a Work Item stays within its budget and
      excludes files unrelated to the Work Item, with no whole-repository dump.
- [ ] [S-17] `TestKnowledgeRecall`: a recorded project decision appears in the assembled context
      of a later related agent run.
- [ ] [S-18] `TestRepoConfigGoverns`: a repository's .novaforge configuration governs agent
      behaviour and gates, and a malformed configuration fails loudly rather than silently
      disabling enforcement.
- [ ] [S-19] `TestSwarmDependencyOrder`: an epic decomposes into dependency-ordered subtasks
      assigned to specialized agents, and a failed prerequisite blocks its dependents.
- [ ] [S-20] `TestMaintenanceProposesWorkItem`: a dependency with a known CVE produces a
      proposed Work Item without the fix being executed unapproved.
- [ ] [S-21] `TestOpenAPICoverage`: every REST route is described in the OpenAPI document, and
      the check fails if a route is added without one.
- [ ] [S-21] `TestCLIFullLifecycle`: the nf CLI creates a repository, pushes, opens a run,
      inspects gates, and merges, with no GUI involved.
- [ ] [S-22] `TestHelmDeploy`: the Helm chart deploys every service to a Kubernetes cluster and
      all services report healthy.

## Open questions
