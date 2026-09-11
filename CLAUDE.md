# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Current state

This repository contains **no source code**. It holds a single design document,
`NovaForge_AI_Native_Git_Platform.md`, on a fresh single-commit history. A previous Go +
React implementation existed and was deliberately deleted along with its history
(2026-09-11); do not try to recover or reference it.

Consequently there are no build, lint, or test commands yet. Do not invent them — add them
to this file as the corresponding tooling is actually introduced.

## What NovaForge is

A self-hosted Git platform where humans and AI agents are both first-class contributors.
Git stays fully standard-compatible (clone/fetch/push, SSH + HTTPS); NovaForge is the
**engineering control plane** layered above it.

The single load-bearing idea, which most design decisions follow from:

> The engineering system — not the agent — controls permissions, context, verification, and
> merge authority.

Practical consequences worth keeping in mind when implementing anything here:

- Agents never get broad personal access tokens. They get scoped capabilities (e.g. write
  only to `agent/NF-182`, staging deploy yes, production no), enforced by the platform.
- Agents never decide their own permissions or approvals; policy is evaluated outside the
  model.
- Quality gates are enforced **after** an agent declares completion, and the agent must not
  be able to bypass them.
- An agent must not be both author and sole reviewer of a change — independent reviewer,
  security, test, and architecture agents review it, ideally on different models to avoid
  correlated failure.
- Never dump a whole repository into a model. Context is assembled per Work Item via
  lexical + symbol + dependency-graph + semantic search plus Git history, then reranked.
- Store observable actions and evidence for provenance — never model chain-of-thought.

## Intended architecture (from the design doc)

Read `NovaForge_AI_Native_Git_Platform.md` before designing anything; it is the source of
truth. The short version:

**Backend** — Go, as a modular monolith initially, with `cmd/` (`novaforge`, `runner`, `nf`
CLI) and `internal/` split by domain (`auth`, `git`, `repositories`, `work`, `reviews`,
`agents`, `runners`, `gates`, `ci`, `mcp`, `indexing`, `knowledge`). OpenAPI (`api/openapi.yaml`)
is the API contract.

**Frontend** — React + TypeScript + Vite, TanStack Router/Query, Shadcn/ui, Monaco.
Transport rule: REST for normal operations, SSE for live agent/CI events, WebSockets **only**
for genuinely interactive sessions.

**Infrastructure** — PostgreSQL, Redis (NATS possible later), native Git repositories,
Docker/Kubernetes for execution, Tree-sitter/LSP/SCIP for code intelligence.

**AI layer** — all model work goes through [`go-ai-sdk`](https://github.com/azrtydxb/go-ai-sdk)
(model abstraction, streaming, tool calling, structured output, agents, approvals, MCP
clients, embeddings, reranking, telemetry). **NovaForge itself must contain no
provider-specific AI logic.** FastLLM → vLLM → DGX Spark/Qwen sits underneath for routing and
inference; local/air-gapped AI is a primary deployment target, not an afterthought.

### Domain concepts that differ from a normal Git host

- **Work Items** replace basic issues — typed (Feature, Bug, Refactor, Security, Tech Debt,
  Research, Architecture, Upgrade, Incident, Docs) with explicit goal, acceptance criteria,
  constraints, and `required_gates`. Assignable to humans or agents.
- **Pull Requests are Engineering Runs** — they present plan, change impact, and proof
  (tests, type check, security, API compatibility, architecture gate), not just a diff.
- **Engineering Graph** — models relationships (symbol → service → API → schema → test →
  Work Item → ADR → deployment → owner → incident), not just files and commits.
- **Agent Runs** execute in ephemeral, isolated environments (Docker → Kubernetes →
  eventually Firecracker). Git changes, events, test evidence, and artifacts persist; the
  environment is destroyed.
- **Typed tools over shell** — agents call audited tools (`repo.search`, `repo.get_symbol`,
  `workspace.write_file`, `git.commit`, `ci.run_test`, `work.comment`, `gate.status`, …)
  rather than getting unrestricted shell access.
- **MCP in both directions** — agents consume approved external MCP servers, and NovaForge
  exposes its own so external agents (Claude Code, Codex) can drive it.
- **Per-repo agent config under source control** in `.novaforge/` (`project.yaml`,
  `agents/`, `gates/`, `context/`, `mcp/`) so AI behavior is reviewable and reproducible.

### Build order

The doc specifies six MVP phases; respect the ordering rather than jumping ahead:
Git foundation → Work & CI → Agent runtime → Governance (gates, capabilities, provenance)
→ Intelligence (graph, indexing, retrieval, knowledge) → Software factory (swarms,
autonomous maintenance, policy auto-merge).

Section 25 lists what to deliberately **not** build early: large wiki systems, portfolio
management, full Kubernetes management, observability suites, many SCM integrations,
complex enterprise project management.

## Commit gate

A procoder hook runs on commit in this repository and **blocks** on findings. Two behaviors
that will otherwise surprise you:

- Commits carrying AI-attribution trailers (`Co-Authored-By: Claude …`) are rejected, and
  the gate inspects history, not just `HEAD` — a bad commit anywhere in the range blocks
  the commit.
- When a turn ends by putting a decision to the user, the gate requires that decision to be
  recorded in `.procoder/ask/decisions.md` and asked via the structured question tool.
