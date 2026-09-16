# Decisions

All decisions below are answered. No question is waiting on a human.

## Performance-regression baseline policy

Answered 2026-09-16: **Option 1**, explicitly selected by the user. Compare
benchmark evidence from the latest eligible default-branch CI run with the
previous comparable successful default-branch run. Require matching benchmark
identity, units and execution-environment metadata. Missing or incomparable
evidence means unavailable, never zero or a guessed baseline.

A manually pinned baseline was not selected. Coverage's existing contract
compares successive evaluations and is unchanged. This records the chosen
policy, not completion of the still-missing production benchmark input.

## Commit and deploy verified completion-audit fixes

Answered 2026-09-16: **Yes**, to the recommended option. The user authorizes
committing and deploying each verified completion-audit batch to the existing
kw deployment without asking again for every batch. Review, tests and the
commit gate precede deployment; image preflight and Helm ownership remain
mandatory, followed by cluster acceptance.

Destructive changes and new product/security decisions still require separate
approval. The alternative of holding commits and deployments for per-batch
approval was not selected.

## What happens next in the now-empty NovaForge repo

Answered 2026-09-11: **Nothing for now**, later superseded by explicit `/init`,
`/procoder:spec`, `/procoder:plan`, and `/procoder:todo` invocations, and finally by a
direct instruction to build the whole backend autonomously. Superseded — no longer open.

## Committing the 67 seeded task files, and the missing procoder templates

Answered 2026-09-11: **Commit the tasks and generate the templates.** Done in commit
e491a47, which added the 67 todo files, the procoder templates, and a copy of the pull
request template at `.github/PULL_REQUEST_TEMPLATE.md`.

## Build environment, after Docker Desktop was found broken

Answered 2026-09-11 by direct instruction: **use the kw cluster, and nexus rather than zot.**
Implemented in `hack/env.sh`:

- Images build on the in-cluster BuildKit at `tcp://192.168.10.130:1234` over mTLS, using the
  client certificate from the `buildkit-client-tls` secret. No local Docker daemon.
- Images push to the nexus registry at `192.168.10.131:5000` under the `novaforge/` prefix.
- PostgreSQL, Redis, and MinIO run in the `novaforge-dev` namespace, exposed as
  LoadBalancer services so the workstation can run integration tests against them.
