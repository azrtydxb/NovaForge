# GUI redesign: implement the claude.ai NovaForge redesign across all screens

Status: open
Created: 2026-10-10

## Description

The claude.ai design project "NovaForge GUI redesign" (37 artboards) replaces the
GUI: a five-group sidebar, a command-palette header with a New menu, theme
toggle and inbox, the desk-style Home, a GitHub-grade repository browser, Work
as list+board, the engineering-run detail, CI with a pipeline graph, agents
with budgets, swarm waves, Rules & gates, People & teams, profile, account,
sign-in, and a real notification inbox behind it.

## Acceptance criteria

- [ ] The design was actually read: artboards extracted from the artifact, not
      reconstructed from titles.
- [ ] The shell matches the design: grouped rail with badge counts, ⌘K palette,
      New menu, theme toggle with a real light palette, inbox badge.
- [ ] The Inbox is a real backend feature: store, RPCs, edge routes, at least
      three publishers, dedupe so a retried publish re-notifies nobody.
- [ ] No screen renders invented data: where the platform has no endpoint, the
      screen says "not available in this deployment".
- [ ] Go: build, vet, gofmt clean; full suite no failures; inbox suites proven
      red→green against a real database.
- [ ] Web: tsc clean, vite build clean, prettier clean.

## Evidence

<!-- Filled at close time. -->
