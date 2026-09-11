# Decisions

## What happens next in the now-empty NovaForge repo

Answered 2026-09-11: **Nothing for now** — the repo stayed a doc-only starting point. Later
superseded in part by an explicit `/init`, `/procoder:spec`, `/procoder:plan`, and
`/procoder:todo`, which produced CLAUDE.md, the backend-platform spec, six plans, and 67
seeded tasks. No implementation code has been written; that part of the answer still stands.

## Committing the 67 seeded task files, and the missing procoder templates

The 67 files under `.procoder/todo/` are untracked. The gate also reports three missing
`.procoder/github/` templates (pull request, commit, workflow) as non-blocking hygiene
findings, which `procoder templates` would generate.

- **Commit the tasks and generate the templates** — one commit carrying both.
- **Commit the tasks only** — leave the templates for whenever a PR workflow actually matters.
- **Neither yet** — leave everything untracked and start building instead.
