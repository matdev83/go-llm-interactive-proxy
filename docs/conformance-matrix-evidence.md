# Conformance Evidence - TCKs and Bounded Sentinel

<!-- architecture-contract: non-cartesian-release-evidence -->

The historical frontend-by-backend matrix is migration evidence only. It is not an authoritative release invariant and must not be treated as permanent architecture. Current release evidence is additive: frontend TCK, canonical-core TCK, backend-family TCK, provider-profile certification, connector TCK, independent protocol compliance, and a bounded real-stack sentinel. The OpenResponses protocol-owned gate is available through make test-openresponses-compliance; make qa runs its static wiring and evidence check.

## Phase 1 Characterization Status

The original live `AllCells()` characterization and its matrix-driven executable tests were intentionally retired in Phase 6 when Cartesian completeness ceased to be a release invariant. They are not silently expected to run at the current head. The durable replacement is the checked-in historical inventory at [`baseline_cartesian_inventory.json`](../internal/testkit/conformance/testdata/baseline_cartesian_inventory.json), which pins the 5-frontends × 9-backends inventory, 45-cell count, required feature IDs, backend kinds, ABI versions, and legacy-surface file metrics. The historical Phase 1 diagnostic row contract is preserved by [`phase1_inventory_baseline.json`](../internal/core/diag/testdata/phase1_inventory_baseline.json), while the complete current compatibility projection is locked separately by [`current_inventory_snapshot.json`](../internal/core/diag/testdata/current_inventory_snapshot.json) and the regressions in `internal/core/diag/inventory_test.go`.

The tagged `architecture_red` target is likewise a migration ratchet, not the old 45-cell count gate. Its non-Cartesian test now verifies that the bounded sentinel remains present and limited (currently 1–8 cases), while the historical 45-cell debt is represented only by the pinned inventory and deletion metric. This is intentional: release correctness no longer requires rebuilding the full frontend×backend product.

Do not restore `AllCells()` merely to recreate the retired release architecture. Update the historical inventory or diagnostic golden only when an intentional compatibility/baseline change is reviewed.

## Current Entry Points

| Subject | Contract | Evidence |
|---|---|---|
| Frontend | `internal/testkit/contract/frontend` | Capturing executor, canonical calls, wire output, auth/limits/errors/cancellation |
| Canonical core | `internal/testkit/contract/core` | Requirements, dialect admission, projections, failover freeze, commitment, terminal semantics |
| Backend family | `internal/testkit/contract/backend` | Capability-selected positive scenarios and hard-negative zero-upstream proof |
| Provider profile | `internal/providerprofiles` | Typed schema, family binding, effective capabilities, endpoint/auth/model behavior, bounded scale |
| Executable connector | `pkg/lipsdk/backendplugin/contracttest` | Negotiation, configure, execute, cancellation, close, semantic carrier preservation |
| Protocol wire behavior | Protocol-owned compliance/refclient/refbackend suites | Independent wire and provenance evidence |

Run `make parity-checks` for the TCK certifications, protocol-owned parity suites, and bounded integration evidence. Tagged integration evidence uses `-tags=integration` and remains independent of provider-profile count.

The reusable root-module contract packages live under `internal/testkit/contract/`; the retained protocol/reference harness is under `internal/testkit/conformance/`.

## Sentinel Policy

The sentinel is an explicit, deterministic set of real frontend -> core -> backend paths. It protects composition-root registration, route mounting, middleware order, immutable generation assembly, representative family behavior, and connector-host wiring. It is not a semantic replacement for the TCKs, and a profile addition inside an existing family cannot add a sentinel case.

## Migration Evidence

During migration, protocol-specific fixtures may retain detailed scenario classifications and independent emulator checks. They may be sampled or invoked explicitly, but no release gate may construct the complete registered frontend-by-provider product or require one evidence object per provider. Generated schemas and independent test/reference files are valid evidence breadth; the change-surface report classifies them separately from canonical/core/ABI/composition coupling.

The complete-product harness and its old row/column descriptions are deliberately not documented here as current architecture. See [`docs/extension-authoring.md`](extension-authoring.md) for profile/family/connector decisions, TCK usage, canonical promotion, ABI evolution, and change-surface review.
