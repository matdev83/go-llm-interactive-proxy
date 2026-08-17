# Brownfield Design Validation

## Result

**GO FOR DESIGN READINESS**

The design addresses the four blocking post-merge defects without folding the subsequent architecture-deletion project into the same implementation PR.

## Validation Against Kiro / Repository Guardrails

### Core vs plugin ownership — PASS

All new policy/data contracts stay in `internal/core/billing`; runtime only captures B2BUA facts and terminal ownership. No provider SDK semantics move into core.

### Canonical model neutrality — PASS

No `pkg/lipapi` request/event contract change is required. Billing sequence is internal billing/B2BUA metadata.

### Streaming-first behavior — PASS

No change to canonical stream semantics. Terminal usage append remains terminal-side and may not trigger post-output retry.

### Provider SDK leakage — PASS

No provider SDK dependency is introduced. Existing backend `FinalizeBilling` edge remains the evidence source.

### No retry after output — PASS

The design explicitly preserves current append failure behavior and adds no financial decision to Recv.

### Brownfield persistence safety — PASS after remediation

The first draft assumed the sequence could simply become required. Validation rejected that because existing durable leg rows lack sequence and opaque B-leg IDs cannot recover it. The design now uses nullable legacy presence, versioned fingerprint compatibility, and fail-closed rating when sequence is semantically required.

### Customer/provider independence — PASS after remediation

The first draft proposed only passing model pricing into `RateCall`. Validation found this would still leave customer settlement dependent on combined `SnapshotsFor` operator-rate lookup. The design now splits customer and operator snapshot resolution.

### Runtime boundedness — PASS after remediation

The first draft considered explicit collector eviction. Validation rejected that as lifecycle-fragile because four maps would still require synchronized cleanup. The design now makes all such state request/BillingCallID-scoped so normal reachability expresses ownership.

## Key Risks and Mitigations

| Risk | Severity | Mitigation |
|---|---:|---|
| Existing rows become unreadable after fingerprint change | High | v1 legacy replay + v2 sequence-aware fingerprint |
| Incorrect legacy sequence backfill | Critical | never infer sequence; fail closed only when policy needs order |
| Parallel completion order mistaken for attempt order | Critical | capture `b2bua.BLegRecord.Seq` at allocation/terminal record |
| Missing operator rate blocks customer | High | customer-specific snapshot resolver |
| Call-scoped object lost across retry/interleaved paths | High | allocate once in prepared request and thread pointer through all B-legs |
| Lock held during backend finalization | Medium | single-flight entry publication under lock, external call outside lock |
| Regression to old monetary hold | Critical | retain existing architecture ratchets |

## Scope Review

The following findings are intentionally deferred to `billing-architecture-final-convergence`:

- native rating directly on `CallUsageRecord`/`CallLegUsageRecord`;
- deletion of `TurnUsageRecord`/`LegUsageRecord`;
- old TUR/LUR table retirement;
- `ReservedNano` domain cleanup;
- money-capable UsageAuthority narrowing;
- provider COGS account-lock decoupling;
- terminal spool redesign;
- stronger simplification LOC/deletion ratchet.

Deferring those is appropriate because none is necessary to correct the immediate wrong-charge or memory-retention defects.

## Final Quality Gate

The design is implementation-ready **after normal spec approval**. Tasks must preserve TDD order and the successor spec must not be implemented concurrently with this one.
