# Phase 5 review

- Verdict: APPROVED.
- Scope: parent tasks 5.1–5.4 only.
- Boundary: billing/runtime B2BUA lineage, terminal evidence, strict sink propagation, narrow COGS/retail selection, existing billing persistence, and tests. No Phase 6 measurement, provider parsing, general rating/reconciliation, public host binding or cutover is implied.

## Requirement review

- Each request-scoped inference observation remains rooted at its concrete B-leg while BillingCallID, A-leg, attempt and provider identities remain correlation lineage. Same-A-leg later calls retain fresh BillingCallIDs and do not mutate prior closure.
- Attempt-owned stream, sideband and finalizer evidence survives as bounded source-separated V2 observations; exact replay deduplicates, changed payload conflicts remain visible, and V1 evidence is only a compatibility projection.
- Terminal sink failures propagate through detached bounded persistence contexts and do not cause post-output upstream retry. The existing TerminalUsageSink remains the single strict terminal seam.
- Operator COGS includes all executed operator-payable attempts, excludes never-started/BYOK, avoids inclusive parent-child double counting, preserves native currencies, and reports partial known subtotal when evidence is unavailable. Surfaced-only retail selection stays independent and call-scoped fees apply once.
- Root review rejected the initial synthetic `lip.runtime` store identity. The repair freezes an internal trusted StoreID from the authoritative composition and proves the resulting observation appends to the matching Phase 4 journal while a different store rejects it.
- Native resource/account-window evidence remains non-B-leg and non-payable until explicit allocation. Bots do not fabricate ownership; the positive allocation contract is owned by parent task 11.2.

## Fresh verification

- `go test -count=1 ./internal/core/billing/... ./internal/core/runtime/... ./internal/infra/billingstore/... ./internal/infra/metering/journalstore/...` — PASS.
- Focused trusted-store runtime and runtimebundle composition tests — PASS.
- `go vet` across the touched billing/runtime/billingstore/journalstore/runtimebundle packages — PASS.
- `gofmt -l` over touched package trees — no output.

The repository-wide archtest package still has recorded baseline failures unrelated to this phase; targeted no-stream-money guards pass. No Phase 5 requirement-blocking finding remains.
