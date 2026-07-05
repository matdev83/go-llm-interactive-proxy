---
type: architecture
title: Routing and Orchestration
description: Core-owned route planning, selector syntax, failover, parallel races, TTFT budgets, and B2BUA recovery.
stack: [go]
tags: [routing, orchestration, b2bua, failover]
status: active
---

# Routing & Orchestration

Core-owned product differentiator. Not optional - one of the main reasons LIP exists.

## Route Planning

- **Ordered failover** (`|`): left-to-right candidate attempts
- **Weighted routing**: deterministic under controlled randomness
- **Parallel races** (`!`): multiple B-legs simultaneously; losers cancelled
- **Handicaps** (`[handicap=N]`): start delays in parallel groups
- **TTFT budgets** (`{ttft_timeout=N}` / `[ttft_timeout=N]`): time-to-first-token limits

## Selector Features

```
model1|model2                   # failover
modelA:0.7|modelB:0.3           # weighted
modelX!modelY                   # parallel race
modelX[handicap=500]!modelY     # race with handicap
model{ttft_timeout=30}          # global TTFT budget
```

- Model aliases rewrite full selector strings before parsing
- Per-leaf query generation params override matching per-request body/canonical options
- Incompatible selector forms fail early (parallel `!` cannot mix with `^`, weights, or `[first]`)

## B2BUA Recovery (Pre-Output Only)

1. **Only pre-output** failures may be swallowed
2. **Once visible output begins**, attempt is committed - no silent failover
3. Every backend attempt recorded in lineage
4. Operators see which attempt surfaced and which were swallowed
5. Parallel losers cancelled/closed without leaking goroutines

## Lineage Model

- **A-leg:** one logical client request / continuity context
- **B-leg:** one backend attempt within that logical request

Lineage records answer: route plan, candidates attempted, why failures/losses/exclusions occurred, which attempt produced surfaced output, did visible output start before failure.

## First-Request Session Steering

First request of a session may follow different route than later turns. Consumed once per session continuity context.

## Routing Health / Circuit Breaker

Can exclude candidates before planning or during failover. Route plan remains observable. State is core-owned, not backend-local.

## Continuity Storage

- **Memory** (`continuity.store: memory`): default, single-process, with TTL and max legs
- **SQLite** (`continuity.store: sqlite`): durable continuity via `internal/core/continuity/sqlitestore/`
