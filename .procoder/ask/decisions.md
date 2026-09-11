# Decisions

All decisions below are answered. No question is waiting on a human.

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
