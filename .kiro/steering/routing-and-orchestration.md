# Routing and Orchestration (Steering)

## Purpose

Routing and orchestration are not optional features in this project. They are the main product differentiators and therefore part of the core runtime contract.

This file captures the durable rules for:

- route planning,
- weighted selection,
- ordered failover,
- parallel backend races,
- TTFT budgets and handicaps,
- first-request session steering,
- B2BUA-like pre-output recovery,
- attempt lineage and diagnostics.

## Core Ownership

The core runtime owns:

- selector parsing,
- model alias expansion,
- candidate resolution,
- health/exclusion-aware planning,
- retry and failover eligibility,
- parallel-race coordination,
- TTFT budget enforcement,
- attempt sequencing,
- swallowed vs surfaced outcome tracking,
- request-level lineage IDs and branch metadata.

Plugins may supply information or policy inputs, but they do not own the orchestration contract.

Hexagonal implication:

- driving adapters may call concrete core executor or use-case services directly when that remains a clean boundary,
- driven adapters may implement narrow consumer-owned seams around backend opening, continuity, and observation,
- do not introduce orchestration interfaces only for symmetry if the existing seam is already replaceable and testable.

## Route Planning Rules

Durable expectations:

- explicit backend/model selectors remain first-class,
- ordered failover is left-to-right and testable,
- weighted routing is deterministic under controlled randomness,
- parallel routing races candidates without committing losers,
- TTFT budgets are satisfied only by client-visible canonical output, not keepalive/warning/usage events,
- health/exclusion state affects candidate eligibility,
- the chosen route plan is observable.

## Selector features

The current selector language includes these core-owned behaviors:

- ordered failover (`|`),
- weighted groups and first-request annotations,
- parallel groups (`!`) that race multiple B-legs,
- per-leg `[handicap=N]` start delays in parallel groups,
- global and per-leaf `{ttft_timeout=N}` / `[ttft_timeout=N]` budgets,
- model aliases that rewrite full selector strings before parsing.

Mixing incompatible selector forms must fail early. In particular, parallel `!` groups cannot be mixed with `^`, weights, or `[first]` in the same arm.

## First-Request Session Steering

The system supports the concept that the first request of a session may follow a different route than later turns.

This must remain explicit and testable.

Rules:

- first-request semantics are consumed once per session continuity context,
- later turns do not accidentally re-trigger first-request behavior,
- invalid selector annotations are rejected early.

## Model alias and health policy inputs

The runtime has two important pre-planning inputs that remain core-owned:

- **Model aliases** rewrite full selector strings through explicit regexp rules before parsing; invalid rules fail during startup validation.
- **Routing health / circuit breaker** state can exclude candidates before planning or during failover, but the resulting route plan must remain observable.

These inputs must not become backend-local behavior. They influence eligibility and planning, not protocol translation.

## B2BUA-like Recovery Semantics

This project intentionally goes beyond simple proxying.

One logical client request may create multiple related backend attempts when a recoverable failure happens **before** client-visible output begins.

Hard rules:

1. **Only pre-output recoverable failures may be swallowed.**
2. **Once visible output begins, the attempt is committed.** No silent failover.
3. **Every backend attempt must be recorded in lineage.**
4. **Operators must be able to see which attempt was surfaced and which were swallowed.**
5. **Recovery policy belongs in the core, not duplicated across backends.**
6. **Parallel losers must be cancelled/closed without leaking goroutines or corrupting lineage.**

## Lineage Model

Use the following mental model:

- **A-leg:** one logical client request / continuity context
- **B-leg:** one backend attempt within that logical request

Lineage records should make these questions answerable:

- Which route plan was computed?
- Which candidates were attempted?
- Why did a candidate fail, lose a race, time out, or get excluded?
- Which attempt produced surfaced output?
- Did visible output start before the failure?

## Secure session interaction

Secure sessions sit before routing execution as an authority and evidence layer:

- client-provided session identifiers are hints until secure-session BeginTurn validates or creates proxy-owned state,
- A-leg continuity and resume authority must not be forged from frontend wire fields,
- session denial happens before upstream work starts and must be surfaced as a deterministic capability/security outcome,
- secure-session recording augments, but does not replace, B2BUA attempt lineage.

## Hooks and Reserved Seams

The runtime must remain ready for advanced orchestration extensions without depending on them.

Reserve stable seams for:

- submit hooks that can annotate or reject before execution,
- request/response part altering hooks,
- route hints that influence planning through typed contracts,
- tool reactors that may observe, swallow, rewrite, or replace tool-call flows,
- completion gates that can make typed buffered decisions,
- auxiliary clients for plugin-owned sub-calls with lineage,
- traffic observers and privileged capture sinks,
- observers that record diagnostics/metrics,
- model inventory/capability providers that help eligibility decisions without leaking provider SDK types.

These seams may influence runtime decisions through typed contracts, but the core must not know plugin-private semantics.

Prefer keeping these seams close to the consuming orchestration capability. Avoid central catch-all `ports` or `services` packages that mix unrelated routing, recovery, and observation concerns.

## Continuity and lineage storage

B2BUA A-leg continuity and attempt lineage flow through `b2bua.Store` and continuity managers.

- **Default configuration** uses an in-memory store (`continuity.store: memory`), which matches single-process operation and the sample `config/config.yaml`.
- **Optional SQLite** (`continuity.store: sqlite`) provides durable continuity metadata via `internal/core/continuity/sqlitestore/`; some tuning fields apply only to the in-memory backend (see config validation and `internal/infra/runtimebundle` package docs).
- `internal/plugins/stores/` is an intentional future plugin seam and may remain sparse; it is not the current SQLite implementation location.

Routing health, exclusions, and related orchestration state remain core-owned; distributed coordination beyond explicit store implementations is still not a v1 product guarantee.

## Orchestration Memory Rules

When updating this file:

- preserve the product-defining semantics,
- keep policy rules explicit,
- avoid baking temporary implementation details into steering,
- update whenever the core orchestration contract changes materially.

---
_Updated 2026-04-23: pragmatic hexagonal ownership notes for routing, observation, and orchestration seams._
_Updated 2026-04-26: added model-alias, routing-health, secure-session, and stage-four seam guidance._
_Updated 2026-06-27: added current selector features, parallel routing, TTFT budgets, and clarified SQLite store location vs future store-plugin seam._
