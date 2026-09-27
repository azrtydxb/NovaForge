# Gap closure G03: durable run and CI webhooks

Status: open
Created: 2026-09-27

## Description

Publish Engineering Run transitions and terminal aggregate CI outcomes from their
owning services. Deliver signed, scoped notifications with stable IDs and durable
retry limits, including recovery after Redis outages and worker restarts.

## Acceptance criteria

- [ ] Committed run transitions and CI results create durable publication intent; rolled-back changes do not.
- [ ] Service entrypoints relay their own outboxes to Redis without cross-schema reads.
- [ ] Hooks receive selected events with stable IDs and signatures; other organizations receive nothing.
- [ ] Retry bounds survive worker restarts and committed events survive Redis outages.
- [ ] GUI/API identify the supported events and cluster acceptance exercises actual transitions.

## Evidence

- The existing push-only consumer and owner transition paths were inspected before implementation.
