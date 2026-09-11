# NovaForge

## AI-Native, Self-Hosted Git Engineering Platform

> **NovaForge is a self-hosted Git platform where humans and AI agents
> build software together, with isolated execution, persistent
> engineering context, and quality gates agents cannot bypass.**

## 1. Vision

NovaForge should not be another Gitea/GitLab clone with an AI chat box.
Git remains the source of truth, while NovaForge becomes the
**engineering control plane** for human and AI software teams.

Core principles:

-   Standard Git stays fully compatible: clone, fetch, pull, push, SSH,
    HTTPS, LFS, branches and tags.
-   Humans and AI agents are first-class contributors.
-   Agents operate with explicit capabilities rather than broad access
    tokens.
-   Agents work in isolated, ephemeral environments.
-   Engineering policy is enforced outside the agent.
-   Local and air-gapped AI is a primary deployment model.
-   Every AI-produced change carries provenance and engineering
    evidence.

## 2. Architecture

``` text
                         NovaForge
                    Go Control Plane
                           |
       +-------------------+-------------------+
       |                   |                   |
       v                   v                   v
   Git Platform       Work / Reviews      Agent Runtime
                                               |
                                           go-ai-sdk
                                               |
                              +----------------+----------------+
                              |                |                |
                           FastLLM          OpenAI          Anthropic
                              |
                            vLLM
                              |
                       DGX Spark / Qwen

       + Engineering Graph
       + ProCoder Gates
       + CI / Runner System
       + MCP Server
```

## 3. Technology Stack

### Frontend

-   React
-   TypeScript
-   Vite
-   TanStack Router
-   TanStack Query
-   Shadcn/ui
-   Monaco Editor

Use REST for normal operations, SSE for live agent/CI events, and
WebSockets only for truly interactive sessions.

### Backend

Use **Go** as a modular monolith initially.

``` text
novaforge/
├── cmd/
│   ├── novaforge/
│   ├── runner/
│   └── nf/
├── internal/
│   ├── auth/
│   ├── git/
│   ├── repositories/
│   ├── work/
│   ├── reviews/
│   ├── agents/
│   ├── runners/
│   ├── gates/
│   ├── ci/
│   ├── mcp/
│   ├── indexing/
│   └── knowledge/
├── web/
└── api/
    └── openapi.yaml
```

Suggested infrastructure:

-   PostgreSQL
-   Redis initially; NATS can be considered later
-   Native Git repositories
-   Docker/Kubernetes execution
-   Tree-sitter, LSP and SCIP for code intelligence
-   OpenAPI as the API contract

## 4. go-ai-sdk

Use `https://github.com/azrtydxb/go-ai-sdk` as NovaForge's
**application-level AI framework**.

It should own:

-   model abstraction
-   streaming
-   tool calling
-   structured output
-   agents and delegation
-   approvals
-   MCP clients
-   embeddings
-   reranking
-   middleware
-   telemetry
-   Code Mode

NovaForge should not contain provider-specific AI logic.

``` text
NovaForge
    |
go-ai-sdk
    |
    +-- FastLLM -> vLLM -> DGX Spark / Qwen
    +-- OpenAI
    +-- Anthropic
    +-- Gemini
    +-- NVIDIA
    +-- other providers
```

**go-ai-sdk = application AI abstraction.**

**FastLLM = model routing and inference infrastructure.**

They are complementary.

## 5. Work Items Instead of Basic Issues

Make **Work** the primary unit of engineering intent.

Types can include Feature, Bug, Refactor, Security, Technical Debt,
Research, Architecture, Upgrade, Incident and Documentation.

``` yaml
work:
  id: NF-182
  type: feature

goal:
  Add passkey authentication

acceptance:
  - WebAuthn registration supported
  - WebAuthn login supported
  - Existing TOTP remains functional

constraints:
  - no breaking schema changes
  - REST API remains backwards compatible

required_gates:
  - architecture
  - tests
  - security
  - api-compatibility
```

Work Items can be assigned to humans or agents.

## 6. Agents as First-Class Contributors

Agents should have identities, roles, permissions and histories.

A change should retain provenance such as:

``` text
Agent: novaforge/backend-engineer
Model: local/qwen
Work Item: NF-182
Run: run_83bf81
Human Sponsor: Pascal
```

Store observable actions and evidence, not private model
chain-of-thought.

## 7. Capability-Based Security

Never give an agent a broad personal access token.

``` yaml
repository:
  read: true
  write_branch: agent/NF-182

work:
  read: true
  comment: true

secrets:
  production: false

deployment:
  staging: true
  production: false

kubernetes:
  namespace: nf-182
```

NovaForge---not the model---enforces these permissions.

## 8. Isolated Agent Workspaces

Each Agent Run receives an ephemeral Docker container, Kubernetes pod or
eventually Firecracker VM.

``` text
run-821
├── repository worktree
├── temporary database
├── required services
└── agent runtime
```

Persist Git changes, events, test evidence and artifacts. Destroy the
environment afterward.

## 9. Pull Requests Become Engineering Runs

Instead of merely showing files and comments, show the complete
engineering proof:

``` text
NF-182 - Add Passkey Authentication

Implemented by: Qwen Backend Agent

PLAN
✓ Analyze architecture
✓ Implement registration
✓ Implement login
✓ Add tests
✓ Run security validation

CHANGE IMPACT
17 files changed
Authentication API affected
User model affected
Session service affected

PROOF
Unit tests             ✓
Integration tests      ✓
Type check             ✓
Security               ✓
API compatibility      ✓
Architecture gate      ✓
```

## 10. ProCoder as the Governance Layer

ProCoder should evolve into NovaForge's engineering policy engine.

``` text
Agent says "complete"
        |
        v
NovaForge Gate Controller
        |
        +-- Tests
        +-- Architecture
        +-- Security
        +-- API compatibility
        +-- Dependencies
        +-- Code quality
        +-- Documentation
        |
        v
Merge allowed?
```

The critical property is that **the agent cannot bypass the gates**.

``` yaml
gates:
  tests:
    minimum_coverage: 80
  architecture:
    forbidden_dependencies:
      - frontend -> database
  security:
    sast: required
    secrets_scan: required
  api:
    breaking_changes: forbidden
```

## 11. Independent Agent Review

Avoid letting the same agent be author and sole reviewer.

``` text
Coder Agent
    |
    v
Change
    |
    +-- Reviewer Agent
    +-- Security Agent
    +-- Test Agent
    +-- Architecture Agent
    |
    v
Human / Auto-Merge Policy
```

Different models may be used for different roles to reduce correlated
failure.

## 12. Repository-Level Agent Configuration

Keep agent configuration under source control.

``` text
.novaforge/
├── project.yaml
├── agents/
│   ├── backend.yaml
│   ├── reviewer.yaml
│   └── security.yaml
├── gates/
│   ├── architecture.yaml
│   └── security.yaml
├── context/
│   ├── architecture.md
│   └── coding-standards.md
└── mcp/
```

This makes AI behavior reviewable and reproducible.

## 13. Strongly Typed Agent Tools

Prefer controlled go-ai-sdk tools over unrestricted shell access:

``` text
repo.search
repo.read_file
repo.get_symbol
repo.get_dependencies
workspace.write_file
git.diff
git.commit
ci.run_test
ci.get_logs
work.get
work.comment
architecture.query
gate.status
```

Tool calls become auditable engineering actions.

## 14. Approval Model

Map go-ai-sdk approvals onto NovaForge policy.

``` text
Read source              -> automatic
Modify isolated workspace -> automatic
Add dependency           -> policy controlled
Change DB schema         -> architecture approval
Access temporary secret  -> explicit approval
Deploy staging           -> policy controlled
Deploy production        -> human approval / forbidden
```

The model never decides its own permissions.

## 15. MCP in Both Directions

NovaForge agents can consume approved external MCP servers:

``` text
NovaForge Agent
├── Kubernetes MCP
├── Jira MCP
├── Database MCP
└── Enterprise MCP
```

NovaForge should also expose its own MCP server so Claude Code, Codex
and other external agents can use:

``` text
novaforge.get_work_item
novaforge.search_repository
novaforge.get_symbol
novaforge.create_branch
novaforge.get_review
novaforge.run_ci
novaforge.get_gate_status
```

## 16. Engineering Graph

Traditional Git hosts understand files, commits and PRs. NovaForge
should understand the engineering relationships behind them.

``` text
UserService
├── depends on -> PostgreSQL
├── called by -> API Gateway
├── tested by -> user.service.spec.ts
├── owned by -> Identity Team
├── implements -> ADR-014
├── deployed as -> identity-service
└── changed by -> PR #1827
```

The graph can connect symbols, services, dependencies, APIs, schemas,
tests, Work Items, commits, ADRs, deployments, owners and incidents.

## 17. Intelligent Context Construction

Never dump the whole repository into a model.

``` text
Work Item
   |
   +-- lexical search
   +-- symbol search
   +-- dependency graph
   +-- semantic search
   +-- Git history
   +-- related tests
   +-- architecture decisions
   |
   v
Reranker
   |
   v
Focused Agent Context
```

go-ai-sdk embeddings and reranking can participate directly in this
pipeline.

## 18. Persistent Project Knowledge

Agent memory belongs to the project, not a model vendor.

Store architecture decisions, accepted patterns, previous incidents,
human corrections, recurring review feedback and operational knowledge.

Example:

``` text
AUTH-018

Never validate JWT tokens directly in route handlers.
Use AuthService.ValidateToken().

Reason:
Centralized key rotation and revocation support.
```

Relevant knowledge is automatically included in future agent context.

## 19. AI-Native CI

CI should support deterministic and agent jobs.

``` yaml
jobs:
  test:
    run: go test ./...

  security-review:
    agent: security

  migration-review:
    agent: database-architect

  regression-analysis:
    agent: qa

  documentation-review:
    agent: documentation
```

Agents become legitimate CI workers, but their conclusions remain
subject to platform policy.

## 20. Secret Brokering

Agents should receive short-lived capabilities instead of permanent
secrets.

Example:

``` text
Agent requests staging AWS access
        |
NovaForge policy evaluates request
        |
Temporary credentials issued
TTL: 20 minutes
Production: denied
```

Vault or cloud workload identity can be integrated later.

## 21. Exception-Based Human Review

The goal is not to make humans review every generated line.

Humans should concentrate on:

-   architecture decisions
-   security-sensitive changes
-   breaking APIs
-   unusual dependencies
-   low-confidence decisions
-   failed gates
-   production actions
-   business decisions

A dashboard could show:

``` text
12 agent tasks running
7 ready for automatic merge
2 require human review
1 architecture decision required
1 security gate failed
1 agent blocked
```

## 22. Agent Swarms

A large Work Item can be decomposed automatically:

``` text
Epic NF-220 - Enterprise SSO
├── NF-221 Database changes
├── NF-222 OAuth backend
├── NF-223 Admin configuration
├── NF-224 Frontend
├── NF-225 Documentation
└── NF-226 Integration tests
```

NovaForge can assign these to specialized agents while maintaining
dependency ordering.

This moves the product from "AI coding assistant" toward an **AI
software engineering organization**.

## 23. Autonomous Repository Maintenance

NovaForge can continuously identify:

-   outdated dependencies
-   CVEs
-   flaky tests
-   dead code
-   coverage regressions
-   documentation drift
-   performance regressions
-   architectural violations

It can create proposed Work Items automatically, estimate impact, and
wait for policy or human approval before execution.

## 24. Product UI

The homepage should emphasize engineering activity rather than
repository browsing.

``` text
NOVAFORGE

12 active agents
7 tasks completed today
3 waiting for review
1 gate failure

WORK
NF-183  Add SSO              Agent working
NF-184  Fix checkout issue   Ready to merge
NF-185  Upgrade PostgreSQL   Planning
NF-186  OAuth vulnerability  Needs attention

AGENTS
Backend Engineer   working NF-183
Security Engineer  reviewing NF-186
QA Engineer        testing NF-184
Architect           idle
```

Git is still fundamental, but it becomes infrastructure underneath the
engineering experience.

## 25. What Not to Build Initially

Do not try to reproduce every GitLab feature.

Avoid initially building:

-   large wiki systems
-   portfolio management
-   full Kubernetes management
-   huge observability suites
-   dozens of SCM integrations
-   complex enterprise project management

The first product should remain opinionated.

### Initial Core

``` text
Git
Work Items
Pull Requests / Engineering Runs
CI
Agent Runtime
go-ai-sdk
Engineering Graph
ProCoder Gates
MCP
Model Gateway integration
```

## 26. Suggested MVP Phases

### Phase 1 --- Git Foundation

Repositories, users, organizations, SSH/HTTPS Git, branches, commits,
diffs and basic reviews.

### Phase 2 --- Work and CI

Structured Work Items, PRs, runners, CI jobs, artifacts and live logs.

### Phase 3 --- Agent Runtime

go-ai-sdk integration, Agent Runs, typed tools, isolated workspaces,
streaming events and local FastLLM/Qwen support.

### Phase 4 --- Governance

ProCoder gates, approvals, capability security, agent provenance and
independent review agents.

### Phase 5 --- Intelligence

Engineering Graph, symbol/dependency indexing, semantic retrieval,
reranking and persistent project knowledge.

### Phase 6 --- Software Factory

Multi-agent planning, Work decomposition, agent swarms, autonomous
maintenance and policy-controlled auto-merge.

## 27. Ecosystem

The existing projects fit together naturally:

``` text
                         NovaForge
               Engineering Control Plane
                         |
          +--------------+--------------+
          |              |              |
       ProCoder       go-ai-sdk      Git / CI
          |              |
   quality policy    agent runtime
                         |
                      FastLLM
                         |
                       vLLM
                         |
                  DGX Spark / Qwen
```

### NovaForge

Owns repositories, Work, reviews, CI, agent execution, permissions,
evidence and engineering workflow.

### ProCoder

Defines and enforces what good engineering means.

### go-ai-sdk

Provides the Go-native model, agent, tool, approval, MCP, embedding and
orchestration framework.

### FastLLM

Routes and serves model inference.

## 28. Core Differentiation

NovaForge should not compete on "we also have an AI coding assistant."

Its differentiation is:

> **AI agents are native engineering workers, but the engineering
> system---not the agent---controls permissions, context, verification
> and merge authority.**

The strongest product message is therefore:

> **NovaForge is a self-hosted Git platform where humans and AI agents
> build software together, with isolated execution, persistent
> engineering context, and quality gates the agents cannot bypass.**
