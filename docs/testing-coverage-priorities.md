# Testing coverage priorities (risk triage)

This document supports **realistic** coverage work: **invariants and regressions first**, not line-percent targets. It aligns with [.kiro/steering/testing.md](../.kiro/steering/testing.md) and the **specification bundle** ([spec-bundle-index.md](spec-bundle-index.md)).

## Principles

- Prefer tests that fail when **routing, streaming, B2BUA, capability, or translation** semantics drift—not blanket coverage.
- Reuse **conformance** ([conformance-matrix-evidence.md](conformance-matrix-evidence.md)), **`make parity-checks`** (`-tags=integration` on `internal/testkit/conformance`), fuzz smoke ([release-gates.md](release-gates.md)), and **SB-\*** registries ([spec-bundle-index.md](spec-bundle-index.md)).
- **Non-goals:** per-cell FE×BE golden transcripts for every matrix cell; moving integration conformance into default `go test ./...`; mandatory mutation runs in default CI ([mutation-testing.md](mutation-testing.md)).

## Hotspots (highest impact if wrong)

| Area | Package(s) | Why it hurts |
|------|------------|--------------|
| Executor / routing | `internal/core/runtime`, `internal/core/routing` | Wrong failover, attempt lineage, or post-output behavior affects every frontend × backend path. |
| Stream engine | `internal/core/stream` | Cancellation, buffering, and adapter contracts affect all streaming adapters. |
| HTTP composition | `internal/stdhttp` | Wiring, auth, diagnostics posture—production surface. |
| Hook bus | `internal/core/hooks` | Ordering, fail-open/closed, tool reactors—silent semantic drift. |
| Canonical model | `pkg/lipapi` | Validation and error taxonomy propagate everywhere; fuzz targets exist—add **targeted** cases when bugs appear. |
| Secure session | `internal/core/securesession/*` | Identity and resume semantics; many paths env-gated (Postgres). |
| Plugin SDK facades | `pkg/lipsdk/*` | Stable contracts for out-of-tree plugins; prefer black-box `*_spec_bundle_test.go` style tests. |

## Qualitative coverage notes (not exhaustive)

- **Runtime:** Large precommit executor matrices and orchestration **SB-\*** registry ([spec-bundle-orchestration-scenarios.md](spec-bundle-orchestration-scenarios.md)); prioritize new tests when changing executor or routing policy.
- **Conformance:** Matrix loops and parity suites are **integration-tagged**; default `make test` does not compile them—use **`make parity-checks`** before merging FE×BE-sensitive changes.
- **Stream:** Pending queue, keepalive/cancel contracts, and compaction paths already have focused tests; extend when a **bug class** appears (TDD).
- **Secure session:** Prefer composed `httptest` tests; reserve real Postgres tests for adapter contracts when env is set.

## Gap hypotheses (prioritized backlog, not a commitment)

Use this as a **checklist for PRs** that touch related code—not a mandate to implement everything.

1. **Cross-protocol parity:** Any new matrix row or subset needs iteration coverage under `-tags=integration` per [conformance-matrix-evidence.md](conformance-matrix-evidence.md)—verify locally with **`make parity-checks`**.
2. **New lipapi validation rule:** Add a **minimal** `lipapi` test + optional fuzz seed if the shape is parser-like.
3. **Thin `pkg/lipsdk` packages:** Add small **`package foo_test`** behavioral tests (2–3 packages per PR max) where exported surfaces lack assertions—see existing `*_spec_bundle_test.go` patterns.
4. **Streaming edge cases:** After incidents, add **one** regression test per bug class (cancel, terminal error, queue cap)—avoid duplicating scenarios already in `internal/core/stream/*_test.go`.
5. **SB-\* registry:** When introducing a **new, non-obvious** core invariant test, add a row to the appropriate registry ([spec-bundle-index.md](spec-bundle-index.md)) so `go test -tags=precommit` doc/source checks fail if docs drift.

## Commands (quick reference)

| Intent | Command |
|--------|---------|
| Default PR suite | `make test` (after `make quality-checks` if you changed Go) |
| Full CI-like suite | `make qa` |
| FE×BE conformance only | `make parity-checks` |
| Spec-bundle doc alignment | `go test -tags=precommit ./internal/core/runtime/...` (and sibling packages per [spec-bundle-index.md](spec-bundle-index.md)) |

## Maintenance

Review this file when **major** orchestration or conformance layout changes; update gap hypotheses based on recent incidents or recurring review comments.
