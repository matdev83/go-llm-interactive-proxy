---
type: architecture
title: Continuity and Recovery
description: B2BUA pre-output recovery semantics, lineage, continuity stores, and secure-session interaction.
stack: [go]
tags: [continuity, recovery, b2bua, sessions]
status: active
---

# Continuity & Recovery (B2BUA)

## B2BUA Semantics

One logical client request may create multiple related backend attempts when recoverable failure happens **before** client-visible output begins.

### Hard Rules

1. **Only pre-output** recoverable failures may be swallowed
2. **Once visible output begins**, attempt is committed - no silent failover
3. Every backend attempt recorded in lineage
4. Operators see which attempt surfaced and which were swallowed
5. Recovery policy belongs in core, not duplicated across backends
6. Parallel losers must be cancelled/closed without leaking goroutines or corrupting lineage

## Lineage Model

- **A-leg:** one logical client request / continuity context
- **B-leg:** one backend attempt within that logical request

Lineage records answer: route plan, candidates attempted, failure/loss/exclusion reasons, which attempt produced output, whether visible output started before failure.

## Continuity Stores

| Store | Config | Use Case |
|---|---|---|
| Memory | `continuity.store: memory` | Default, single-process, with TTL and max_legs tuning |
| SQLite | `continuity.store: sqlite` | Durable continuity via `internal/core/continuity/sqlitestore/` |

## Secure Session Interaction

- Client-provided session identifiers are hints until secure-session `BeginTurn` validates or creates proxy-owned state
- A-leg continuity and resume authority must not be forged from frontend wire fields
- Session denial happens before upstream work starts - deterministic capability/security outcome
- Secure-session recording augments, does not replace, B2BUA attempt lineage

## Package Map

| Package | Responsibility |
|---|---|
| `internal/core/b2bua/` | B2BUA continuity store interface + implementations, A-leg tracking |
| `internal/core/continuity/` | Continuity managers (memory store, SQLite store) |
| `internal/core/continuity/sqlitestore/` | SQLite-backed durability for continuity metadata |
| `internal/core/securesession/` | Session authority, BeginTurn, resume, denial, diagnostics |
| `internal/core/leglifecycle/` | B-leg attempt lifecycle |
| `internal/core/lineage/` | Lineage identifiers and records |
