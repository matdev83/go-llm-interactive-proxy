# Brief: billing-host-composition

## Problem
Operators and internal/enterprise hosts have an authoritative billing engine (TUR/LUR, Bun journal, admission, post-turn worker, reports) but no in-tree composition that actually turns it on. Default `lipstd` still does not bill. Maintainers cannot close the product loop without inventing identity, pricebook, store-open, and rating lookup in each host.

## Current State
`usage-record-ledger-billing` is implemented and merged. Runtime seams are admit + terminal TUR handoff (`BillingRuntime`, stamped identity, outbox worker). `ProductionOptions` injects store, admission, identity, and `RatingResolver`. Public `lipruntime.Options` stays non-money. `RatingResolver` has test fakes only. Holds store snapshot **refs**, not bodies. `/admin/billing` is read-only. Account create/funding exist as store methods only. Handoff outbox is process-local memory.

## Desired Outcome
A documented, testable host-composition path can enable authoritative billing without reopening stream, routing, or TUR sealing: map a call to a billing account, open the Bun journal, wire admission, resolve exact pricing/policy/operator snapshots for post-turn rating, and provision/fund accounts through trusted commands (HTTP optional).

## Approach
Keep public `lipruntime.Options` non-money. Add internal composition (and optional `cmd/lipstd` wiring only if requirements explicitly allow it) that injects existing `BuildHost` / `ProductionOptions` ports. Provide a stock `RatingResolver` backed by an immutable versioned snapshot catalog, plus identity helpers that stamp from authenticated principal/session. Do not put snapshot bodies into the journal; keep refs + catalog.

## Scope
- **In**: snapshot catalog + stock `RatingResolver`; identity mapping helpers; store-open + admission adapter wiring recipe; account create / funding / credit-policy as composition or operator API; tests proving an injected host can authorize → execute → seal TUR → rate → journal
- **Out**: payment gateway, invoicing, VAT/FX; stream-time money; public `lipruntime.Options` billing fields unless requirements reopen that fence; durable Bun handoff outbox (optional later; memory still matches “retry while process is up”); changing B2BUA/retry/stream semantics

## Boundary Candidates
- Snapshot catalog (immutable pricing/policy/operator-rate versions)
- Host composition / `BuildHost` wiring
- Operator account provisioning (store commands vs admin HTTP)
- Identity mapping (principal/session → account/authorization IDs)

## Out of Boundary
- Runtime collector, TUR seal, rating formulas, journal invariants (owned by `usage-record-ledger-billing`)
- Connector `FinalizeBilling` money ABI
- Usage-authority quota / concurrency

## Upstream / Downstream
- **Upstream**: `usage-record-ledger-billing` (engine + ports)
- **Downstream**: enterprise hosts, optional later durable outbox, optional public Options if product later wants library injectors to bill

## Existing Spec Touchpoints
- **Extends**: composition notes in `usage-record-ledger-billing` (does not reopen its checked tasks)
- **Adjacent**: archived `usage-accounting-architecture-convergence` (never implemented; non-money quota/rate-limit leftover needs a new spec)

## Constraints
- Fail closed when `accounting.billing.authoritative: true` without complete injection
- No DI container
- Identity stamped once at admission (do not re-resolve at handoff)
- English spec artifacts
