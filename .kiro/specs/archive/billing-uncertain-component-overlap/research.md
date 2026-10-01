# Research and Design Decisions

## Summary

- Feature: `billing-uncertain-component-overlap`.
- Baseline: merged #666 at `6e97e0d9`.
- Discovery: brownfield extension with focused identity, graph-cost, composition, and persistence analysis.
- No external dependency is introduced; local source is authoritative.
- The main assistant rewrote requirements and authored design/tasks. Workers supplied evidence and read-only review, not the final specification.

## Research Log

### Policy clarification

The earlier #666 condition-6 deferral retained nonblocking unknown intersection. In this session the user rejected the proposal to withhold customer charges because overlap was unknown. We therefore preserve charges and introduce structured visibility. Absence of evidence is not evidence of duplicate billing; no permissive/strict monetary switch is part of this spec.

### Existing support authority

Sources: `internal/core/billing/component_rater_support.go`, `component_rater_overlap.go`, `component_rater_quantity_solver.go`, `component_rater.go`.

- The relation predicate is production; only the full pair census/report bridge is test-only.
- The conflict candidate walk visits containment-related pairs, not sibling pairs sharing an ancestor. Promoting that walk's return values to a report would miss the target case.
- The solver's quantity-positive reporter is not the same population as retained positive-charge contributors.
- Relation separation includes branch roots and requires `cover.resolved`; the legacy reporter uses strict descendants and different coverage criteria.
- For v1 use the relation as the one structural definition; leave the legacy path untouched and do not join old uncertainty text to v1 errors. Typed error behavior is preserved.

### Durable identity

Sources: `pkg/lipsdk/economics/component_rating.go`, `valuation.go`, `valuation_context.go`; `internal/infra/billingcompose/catalog.go`; `internal/infra/billingstore/v2_economics_store.go`.

Canonical valuation JSON includes the whole typed DTO and supplies the fingerprint. Durable append compares canonical bytes under the input/context identity. Adding report fields to all old results would create same-identity conflicts. Existing optional frozen `Schemas` material demonstrates the compatible snapshot pattern: omitted fields retain old preimages; same ref/different content fails publication.

Use an optional frozen reporting-version marker, new publication for adoption, optional context/report fields, existing context hashing, and canonical JSON storage. No sidecar or SQL migration is needed. Historical re-rating remains legacy rather than retroactively enriching old financial facts.

### Retail aggregation

Sources: `internal/core/billing/retail_rating.go` at `retailQuantityGroups`, `combineRetailValuation`, `appendRetailLines`, `composeRetailValuation`, `retailCompositeIdentity`.

Different route tariffs can generate the same component IDs. Existing composition renames duplicate line IDs and overwrites the top-level tariff with the base tariff. Report identities must retain source tariff context and component/scope keys, not fragile line IDs. Contexts enter ContextHash so changing only a route-specific reporting version changes the composed interpretation identity. No cross-group pair is inferred.

### Work bounds

An exhaustive unknown-pair report can be quadratic for siblings beneath a single ancestor. The initial worker draft simultaneously required exhaustive pair reporting and no pairwise work; that promise was removed. v1 has finite candidate/graph/output limits, deterministic partial results, and explicit incompleteness. A partial reachability closure yields no verdict, never false separation. Existing monetary analysis is not subject to advisory budgets.

### Architecture capacity

Sources: `internal/archtest/phase20_budget_exactness_test.go`, `billing_convergence_growth.go` and the repository verification harness.

Executed `go test -run 'TestPhase20RefreshedBudgetHeadroomExact|TestBillingEconomicsGrowthAllowanceLive' -v ./internal/archtest`: both PASS. Core audited baseline 143695, ceiling 143720, overlay cap 57726. A new assessor will probably need audited growth allowance or genuine same-scope simplification. Explicit footprint/authorization task precedes production work; no speculative cap increase is authorized by this spec.

## Architecture Pattern Evaluation

| Option | Benefit | Limitation | Decision |
|---|---|---|---|
| No change | Zero contract change | Successful results hide uncertainty | Reject |
| Add error text on success | Small patch | Makes success look like failure; not typed/durable advice | Reject |
| Advisory on every old valuation | Direct visibility | Breaks historical canonical replay | Reject |
| Immutable optional result + frozen reporting version | Fits existing catalog/context/store seams | Requires explicit new publication | Select |
| Separate advisory store/worker | Keeps old DTO unchanged | Second owner, migrations, additional I/O/joins, sync failure mode | Reject |
| New disjointness relationship | More evidence vocabulary | Not necessary for the selected visibility requirement | Defer; not in scope |
| Generic policy/plugin framework | Future strict modes | Unrequested authority and lifecycle expansion | Reject |

## Design Decisions

### Nonblocking reporting, not revenue denial

Billing keeps its existing money decisions. Known conflict safeguards remain; unknown pairs and report limits are advice only. No invented overlap quantity or arbitrary discount.

### Version interpretation at frozen publication

Use `SupportAdvisoryVersion` empty/v1 in tariff material, content-address it, and require new publication rather than upgrading a stored snapshot in place. This is a reporting compatibility mechanism, not a configurable settlement policy.

### Reuse existing structural relation and coverage proofs

Pass the conflict resolver's per-scope coverage verdicts through a private result, use the receiver's compiled program, and initialize context in the caller even with no pairs. Do not add a third coverage resolver or redefine disjointness in a reporter. For v1 remove only redundant legacy unknown-text emission, not quantity checks.

### Bounded assessment is honest about incompleteness

128 reported pairs, 4096 candidate examinations, 65536 additional graph visits per independently rated group. Context/output bounds hold on the final composed valuation; budgets are deterministic, not wall-clock limits. Budget exhaustion yields no in-flight verdict and a contextual incomplete marker. Limits are frozen into v1 semantics; changing them requires a new reporting version.

### Existing durable owner

The canonical valuation payload persists advice with money in the existing append transaction. Same-identity changed advice remains an integrity conflict. All query/detail paths read stored advice, never recompute it. Rollout is reader-first; old workers must not re-append enabled results after discarding unknown JSON fields.

## Risks and Mitigations

- Uncertainty text disagrees with new structure: retain text for legacy only; v1 uses one relation and separate typed report.
- Limits hide pairs: expose incomplete contexts, pin deterministic prefix, never describe partial work as a clean result.
- Route reporting context disappears: retain source contexts and incorporate them in ContextHash before final identity.
- Historical drift: independently pinned old bytes/hashes, six legacy fingerprints, unchanged version-empty path.
- Extra store test cost: shared contract fixtures and canonical parity entry points; Windows `make test-cost` required for delivery.
- Insufficient line capacity: explicit measured/authorized footprint gate before core work; preserve all excess-negative gates.

## Review Evidence

Requirements gate was rerun by the main assistant after replacing the worker draft; 33 EARS criteria cover the clarified policy without new SDK relation semantics. Independent design review requested explicit context initialization, coverage reuse, budget-scope alignment, legacy/v1 diagnostic distinction, no-verdict on exhausted closure, real composition bounds, cost gate, footprint prerequisite, and the existing context-mutation test. Those points are incorporated in design. Final task-graph and quick-workflow sanity outcomes are recorded after review.

## References

- `.kiro/steering/product.md`, `tech.md`, `structure.md`, `testing.md`.
- `.kiro/specs/archive/extensible-usage-economics-reconciliation/requirements.md` (actual overlapping additive charges, not inferred unknown overlap).
- `.kiro/specs/archive/usage-economics-b-leg-multimodal-refinement/` (upstream billing implementation).
- `internal/core/billing/billing_acceptance_vectors_test.go` vector 29 (100/20/30 -> complete 50).
- `internal/core/billing/billing_determinism_replay_test.go` (six pinned historical fingerprints).
- `pkg/lipsdk/economics/phase9_valuation_context_repair5_red_test.go` (context mutation coverage).

## Final Review Resolution

Independent task-graph review verified 33/33 requirement mappings and found concrete implementation prerequisites. The main assistant corrected measured capacity (live core 143700/143720, growth57665/57726), named the authorized footprint surface, used pre-narrowing groups for the constructor bound, preserved existing validation precedence, and specified an independent budgeted cache. A residual publication flaw was verified directly in catalog.go: PutTariff alone cannot satisfy customer pricing-card lookups. The design now defines atomic card-plus-enabled-tariff publication in that existing catalog boundary, leaving core pricing contracts unchanged. Final inline review confirms dependency and boundary alignment; all substantive findings are resolved. No production code has been written.
