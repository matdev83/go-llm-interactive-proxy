# Routing and Orchestration (Steering)

## Purpose

Routing and orchestration are not optional features in this project. They are the main product differentiators and therefore part of the core runtime contract.

This file captures the durable rules for:

- route planning,
- weighted selection,
- ordered failover,
- first-request session steering,
- B2BUA-like pre-output recovery,
- attempt lineage and diagnostics.

## Core Ownership

The core runtime owns:

- selector parsing,
- candidate resolution,
- health/exclusion-aware planning,
- retry eligibility,
- attempt sequencing,
- swallowed vs surfaced outcome tracking,
- request-level lineage IDs and branch metadata.

Plugins may supply information or policy inputs, but they do not own the orchestration contract.

## Route Planning Rules

Durable expectations:

- explicit backend/model selectors remain first-class,
- ordered failover is left-to-right and testable,
- weighted routing is deterministic under controlled randomness,
- health/exclusion state affects candidate eligibility,
- the chosen route plan is observable.

## First-Request Session Steering

The system supports the concept that the first request of a session may follow a different route than later turns.

This must remain explicit and testable.

Rules:

- first-request semantics are consumed once per session continuity context,
- later turns do not accidentally re-trigger first-request behavior,
- invalid selector annotations are rejected early.

## B2BUA-like Recovery Semantics

This project intentionally goes beyond simple proxying.

One logical client request may create multiple related backend attempts when a recoverable failure happens **before** client-visible output begins.

Hard rules:

1. **Only pre-output recoverable failures may be swallowed.**
2. **Once visible output begins, the attempt is committed.** No silent failover.
3. **Every backend attempt must be recorded in lineage.**
4. **Operators must be able to see which attempt was surfaced and which were swallowed.**
5. **Recovery policy belongs in the core, not duplicated across backends.**

## Lineage Model

Use the following mental model:

- **A-leg:** one logical client request / continuity context
- **B-leg:** one backend attempt within that logical request

Lineage records should make these questions answerable:

- Which route plan was computed?
- Which candidates were attempted?
- Why did a candidate fail or get excluded?
- Which attempt produced surfaced output?
- Did visible output start before the failure?

## Hooks and Reserved Seams

The runtime must remain ready for advanced orchestration extensions without depending on them.

Reserve stable seams for:

- submit hooks that can annotate or reject before execution,
- request/response part altering hooks,
- tool reactors that may observe, swallow, rewrite, or replace tool-call flows,
- observers that record diagnostics/metrics.

These seams may influence runtime decisions through typed contracts, but the core must not know plugin-private semantics.

## V1 Storage Assumption

V1 uses in-memory state for route exclusions, session-first-request markers, and B2BUA lineage.

Persistence or distributed coordination may come later behind explicit store interfaces.

## Orchestration Memory Rules

When updating this file:

- preserve the product-defining semantics,
- keep policy rules explicit,
- avoid baking temporary implementation details into steering,
- update whenever the core orchestration contract changes materially.
