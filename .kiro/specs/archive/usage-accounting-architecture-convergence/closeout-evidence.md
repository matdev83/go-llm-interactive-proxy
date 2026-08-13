# Usage Accounting Architecture Convergence Closeout

- **Spec:** `usage-accounting-architecture-convergence`
- **Status:** superseded and archived (never approved; no task started)
- **Superseded by:** `usage-record-ledger-billing` for stream-time money accounting; follow-on `billing-host-composition` for host injection
- **Closeout date:** 2026-08-13
- **Closeout head:** `27bf3202` (`feat(billing-host-composition): injection-only host composition for authoritative billing (#311)`)

## Cross-check vs `usage-record-ledger-billing`

This spec was drafted 2026-08-10 as a **contraction of technical usage accounting**. Requirements and design explicitly put wallets, balances, double-entry journals, and commercial billing **out of scope**. Approvals stayed false and every task checkbox stayed empty.

The next-day spec `usage-record-ledger-billing` is **not a 1:1 replacement** of this document. It owns the financial bounded context this spec deferred, and it also deleted the stream-time financial machinery this spec wanted to retire.

| Intent in this spec | Outcome after billing landed |
| --- | --- |
| Metering facts + one reducer as the sole technical evidence owner | Not implemented here. Authoritative money evidence is sealed TUR/LUR, not a metering-fact settlement pipeline. Residual only if a non-money evidence architecture is still wanted. |
| `accounting.Service` request/attempt lifecycle coordinating rating and authority | Superseded for money: billing owns authorize → execute → TUR seal → rate → journal. Residual only for non-money quota coordination. |
| Retire stream-time pricing, token-ledger writes, runtime economic merge/accumulators | Done by `usage-record-ledger-billing` Phase 8. |
| Customer wallets, holds, journals | Out of this spec; implemented by `usage-record-ledger-billing`. |
| Non-money quota / rate-limit redesign | Explicitly preserved by billing. Production `accounting.authority` YAML still allows token quota and rate-limit rules and rejects monetary `budget` / `spend_cap` / `money_nano`. `billing-host-composition` cited this spec as the adjacent home for that leftover. |

Do not implement `tasks.md` as written. A later non-money quota spec should start from current `usageauthority` plus the billing fences, not from this 2026-08-10 gap analysis against SHA `294fa587`.
