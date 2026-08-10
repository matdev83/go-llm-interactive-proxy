# Implementation Plan

## Current Implementation Checkpoint

Fresh assessment of the uncommitted worktree recognizes **3 of 8 tasks in Phases 1–2 as complete**: 1.1, 1.3, and 2.1. All tasks in Phases 3–7 remain unstarted.

Partial work that must not be treated as complete:

- **1.2:** structural scale and diff scanners now exist and the tagged debt target detects the current Cartesian matrix, but the 1,000-profile assertion still proves only family-count arithmetic and the clean footprint test feeds a hand-built diff rather than obtaining a real Git change. It does not yet prove that generated frontend×profile structures and sentinel growth remain absent for profile #1000.
- **1.4:** ordinary architecture tests pass and the tagged target reports narrowed current route, diagnostics, contribution, ABI, scale, and continuation debts. Structural Go/protobuf ABI tests were added, but contribution/diagnostics authority mutation fixtures remain absent and protocol/provider discovery still relies on a closed identifier list rather than arbitrary identifiers/aliases. The guard set is therefore not yet fully attested.
- **2.2:** the frontend runner and five mounted frontend certifications execute successfully, but bundled descriptors advertise only streaming/tools. Multimodal, reasoning, structured-output, ordered-item, compaction, and extension scenarios therefore run mostly as negatives instead of certifying each frontend's actual positive semantic surface.
- **2.3:** the reusable backend runner, mutation tests, and real standard-family harness exist, but the standard-family certification is RED. Current failures show missing tool lifecycle evidence for OpenAI-family adapters and missing explicit usage presence for Anthropic/Alibaba/Gemini/Bedrock/OpenResponses-compatible families.
- **2.4:** ACP, OpenRouter, and NVIDIA module tests and the root host-adapter packages pass. The public runner still requires callers to supply `StartHost`, while the only real host construction used by representatives imports `internal/infra/backendplugins/adapter`; a genuinely external connector cannot invoke the supported real-host path. The claimed fake external-style end-to-end fixture is also absent.

Current fresh validation evidence:

- Green: Phase 1 characterization packages; ordinary architecture/scale packages (with the root-only enterprise compile gate excluded); canonical-core TCK; frontend TCK plus all frontend packages; root backend-plugin/tool/host-adapter packages; ACP/OpenRouter/NVIDIA module contract tests; `gofmt -d`; `git diff --check`.
- Expected RED: `go test -tags=architecture_red -run 'TestRED_' ./internal/archtest` reports exactly the current five debt categories.
- Unexpected RED: `go test ./internal/testkit/contract/... ./pkg/lipsdk/backendplugin/contracttest/...` fails in `internal/testkit/contract/backend` because current standard-family certifications lack required tool/usage event evidence.

## 1. Freeze Brownfield Behavior and Add RED Architecture Contracts

- [x] 1.1 Freeze current conformance, contribution, route, diagnostics, ABI, and continuation baselines
  - Characterize `BundledFrontendIDs`, `BundledBackendIDs`, `AllCells`, current required feature IDs/scenario IDs, standard/essential/compatible backend lists, frontend route claims, compatible/OpenResponses diagnostics, backend-plugin v1.0–v1.3 negotiation, and continuation store/recorder behavior.
  - Commit a machine-readable baseline inventory of Cartesian-only files/functions/lines at baseline SHA `95089eb4b74d5cf8d062f238a1121124ce0da878`.
  - Record current diagnostic JSON shapes before generic projection changes.
  - Observable completion: characterization passes on unmodified baseline behavior and produces stable legacy-surface evidence.
  - _Requirements: 1.1–1.10, 10.1–10.2, 11.6–11.7, 12.1–12.2, 12.6, 13.11_
  - _Design rules: D1, D12–D14, D16–D18_
  - _Boundary: tests/evidence only_
  - _Depends: none_
  - _Validation: go test ./internal/testkit/conformance/... ./internal/standardplugins/... ./internal/stdhttp/contract/... ./pkg/lipsdk/backendplugin/... ./pkg/lipsdk/continuation/... ./internal/core/continuation/..._

- [ ] 1.2 Add RED non-Cartesian scale and extension-footprint contracts
  - Build synthetic five-frontend / 1,000-provider-profile fixtures.
  - Assert profile additions do not create frontend×profile pair collections, family factories, central compatible/essential list edits, or sentinel growth.
  - Add a provider-profile-only fixture whose expected shared-boundary footprint is zero.
  - Observable completion: tests fail against the current matrix/parallel-registry architecture for the intended reasons.
  - _Requirements: 2.1–2.10, 6.11, 7.5, 12.8, 13.3, 13.7_
  - _Design rules: D2, D5, D7, D15, D18_
  - _Boundary: architecture/test fixtures_
  - _Depends: 1.1_
  - _Validation: go test ./internal/archtest/... ./internal/testkit/..._

- [x] 1.3 Add RED contract-test-kit interfaces and semantic scenario metadata
  - Define test-only/supported dependency-neutral semantic feature IDs, scenario IDs, subject descriptors, certification DTOs, and narrow frontend/backend harness contracts.
  - Keep executable scenario builders out of production runtime packages.
  - Add compile-time tests proving third-party connector-facing contract types do not import `internal` or concrete backend packages.
  - Observable completion: interfaces/metadata compile and RED runner tests specify expected scenario selection/evidence before runners exist.
  - _Requirements: 3.1–3.3, 3.7–3.10, 4.1–4.3, 4.9–4.10, 5.1, 10.10, 13.1_
  - _Design rules: D1–D4, D8, D14_
  - _Boundary: test contract/API definitions_
  - _Depends: 1.1_
  - _Validation: go test ./internal/testkit/contract/... ./pkg/lipsdk/backendplugin/contracttest/..._

- [ ] 1.4 Add RED contribution/profile/ABI/mirror architecture guards
  - Add tests that reject duplicate authoritative extension registries, central protocol-specific route kinds, protocol-ID diagnostic switches, new protocol-named backend-plugin features/proto fields beyond the v1.3 compatibility allowlist, provider profiles imported by core, and duplicate continuation authorities.
  - Add explicit allowlist for current legacy v1.3 OpenResponses ABI symbols only.
  - Observable completion: current known gaps fail the new guards while existing legitimate provider dialect IDs remain allowed.
  - _Requirements: 7.1–7.10, 8.1–8.8, 10.3, 10.11, 11.1–11.9, 13.1–13.4_
  - _Design rules: D7–D13, D18_
  - _Boundary: internal/archtest_
  - _Depends: 1.1_
  - _Validation: go test ./internal/archtest/..._

## 2. Build Frontend, Core, Backend, and Connector TCKs

- [x] 2.1 Implement the canonical-core contract suite
  - Move/reuse canonical semantic fixtures to test requirement derivation, exact matching, item↔legacy projectors, transport admission, frozen failover requirements, output commitment, and terminal validation without real provider adapters.
  - Add deliberate mutation tests proving each core invariant turns RED.
  - Observable completion: core semantic proof is executable without frontend/backend Cartesian cells.
  - _Requirements: 5.1–5.4, 9.3–9.5, 9.9_
  - _Design rules: D1, D3–D4, D9–D10_
  - _Boundary: internal/testkit/contract/core and focused pkg/lipapi/core tests_
  - _Depends: 1.3_
  - _Validation: go test ./internal/testkit/contract/core/... ./pkg/lipapi/... ./internal/core/capabilities/... ./internal/core/runtime/..._

- [ ] 2.2 Implement the frontend TCK runner
  - Implement capturing executor, scripted canonical event stream, common request/error/stream contracts, and certification output.
  - Migrate reusable text/tools/multimodal/reasoning/structured-output/error scenarios from matrix code without importing backends.
  - Keep OpenResponses continuation/WebSocket/compaction lifecycle in protocol-owned tests composed alongside the TCK.
  - Observable completion: all bundled frontends obtain frontend certification without constructing real provider backends.
  - _Requirements: 4.1–4.10, 1.1, 1.7, 5.10_
  - _Design rules: D1–D3, D14, D16_
  - _Boundary: internal/testkit/contract/frontend + frontend tests_
  - _Depends: 1.3, 2.1_
  - _Validation: go test ./internal/testkit/contract/frontend/... ./internal/plugins/frontends/..._

- [ ] 2.3 Implement the backend-family TCK runner with zero-upstream proof
  - Implement capability/dialect-driven positive selection and hard-negative scenarios.
  - Add request/process probe support and canonical event/usage/error/cancel/lifecycle assertions.
  - Provide harness adapters for essential built-ins and compatible-family adapters using current refbackends/httptest providers.
  - Observable completion: each current in-process backend family passes one reusable semantic suite; false capability mutation is caught.
  - _Requirements: 3.1–3.7, 3.9–3.10, 1.2–1.4_
  - _Design rules: D1–D4, D14_
  - _Boundary: internal/testkit/contract/backend + backend tests_
  - _Depends: 1.3, 2.1_
  - _Validation: go test ./internal/testkit/contract/backend/... ./internal/plugins/backends/..._

- [ ] 2.4 Implement the supported executable-connector contract test entry point
  - Drive negotiation/configure/execute/cancel/close through the real backend-plugin host adapter.
  - Reuse the same semantic scenario source as 2.3; do not fork a connector-specific feature catalog.
  - Add at least ACP/OpenRouter/NVIDIA/current connector representatives and a fake external-style connector fixture.
  - Observable completion: connector authors can invoke one supported SDK contract test and receive machine-readable certification evidence.
  - _Requirements: 3.7–3.10, 10.9–10.10, 13.1_
  - _Design rules: D3–D4, D11–D14_
  - _Boundary: pkg/lipsdk/backendplugin/contracttest + connector-host test harness_
  - _Depends: 2.3_
  - _Validation: go test ./pkg/lipsdk/backendplugin/... ./tools/backendplugin/... ./internal/infra/backendplugins/adapter/..._

## 3. Introduce Provider Families and Typed Provider Profiles

- [ ] 3.1 Define the provider-profile v1 schema and RED validation suite
  - Define typed versioned profile contracts for family, endpoint/path policy, credential references, safe headers, model discovery/namespace, tokenizer/accounting, capability/dialect overrides, and closed family quirks.
  - Add refusals for arbitrary transformations, unsafe auth/header combinations, unknown quirks/versions, capability elevation beyond family support, invalid endpoints, and unbounded data.
  - Observable completion: schema tests define safe declarative boundaries before catalog/runtime implementation.
  - _Requirements: 6.1–6.5, 6.7–6.8, 6.12_
  - _Design rules: D5–D6, D9–D10_
  - _Boundary: internal/providerprofiles contract/tests_
  - _Depends: 1.2_
  - _Validation: go test ./internal/providerprofiles/..._

- [ ] 3.2 Implement deterministic embedded provider-profile catalog and family binding
  - Add one source-of-truth profile catalog loaded through `go:embed` or deterministic generated typed values.
  - Bind profiles to existing compatible family adapters without one Go factory registration per provider.
  - Preserve arbitrary custom-compatible user configuration independently from the shipped profile catalog.
  - Observable completion: current/synthetic profiles resolve to family configurations through one compiler and require no provider network/process activation.
  - _Requirements: 6.2–6.7, 6.9, 6.12, 7.5_
  - _Design rules: D5–D8_
  - _Boundary: provider profile catalog + standard composition_
  - _Depends: 3.1_
  - _Validation: go test ./internal/providerprofiles/... ./internal/standardplugins/... ./internal/infra/runtimebundle/..._

- [ ] 3.3 Implement profile certification and 1,000-profile scale proof
  - Certify family binding, endpoint/auth/header/model behavior, effective capability/dialect calculation, and declared quirks without frontend multiplication.
  - Add 1,000-profile synthetic catalog load/validation tests and assert zero provider network/process starts and no per-profile goroutine.
  - Observable completion: profile #1000 costs one profile certification path and does not affect frontend TCK/sentinel counts.
  - _Requirements: 2.2–2.7, 6.7–6.11, 12.8_
  - _Design rules: D2–D6, D15_
  - _Boundary: profile TCK/scale tests_
  - _Depends: 3.2, 2.3_
  - _Validation: go test ./internal/providerprofiles/... ./internal/testkit/contract/backend/..._

- [ ] 3.4 Integrate provider-profile changes with generation compile/reload and diagnostics source data
  - Reuse existing immutable candidate/generation compilation and startup security validation.
  - Prove profile catalog changes rebuild affected family instances without a second mutable registry.
  - Keep profile values secret-safe and provider processes dormant during validation.
  - Observable completion: startup/check-config/reload characterization remains coherent for profile-backed compatible instances.
  - _Requirements: 1.5–1.6, 6.7–6.9, 8.5–8.7_
  - _Design rules: D5–D8_
  - _Boundary: runtimebundle/configreload composition only_
  - _Depends: 3.2_
  - _Validation: go test ./internal/infra/runtimebundle/... ./internal/core/configreload/... ./internal/standardplugins/..._

## 4. Single-Source Contribution Metadata and Generic Projections

- [ ] 4.1 Add focused frontend/backend contribution facets behind RED derivation tests
  - Define registration, route, diagnostic, contract, and compatible-family facets without carrying runtime service bags.
  - Add synthetic contributions proving deterministic derivation, uniqueness, and automatic appearance in declared views.
  - Observable completion: tests specify one-authority behavior before central lists are removed.
  - _Requirements: 7.1–7.9, 13.7_
  - _Design rules: D7–D8_
  - _Boundary: internal/standardplugins or cycle-neutral contribution package_
  - _Depends: 1.4, 2.2, 2.3_
  - _Validation: go test ./internal/standardplugins/... ./internal/pluginreg/..._

- [ ] 4.2 Migrate standard/essential/compatible registration to contribution-derived views
  - Derive `StandardBundle`, essential IDs, compatible family IDs, contract subjects, and provider-profile family bindings from one source.
  - Delete parallel authoritative lists/switches superseded by contribution facets.
  - Preserve current optional-connector rule: optional connector identities are not promoted into essential built-ins.
  - Observable completion: all existing built-ins resolve identically and provider profile additions require no central Go list edits.
  - _Requirements: 7.1–7.10, 2.2–2.5_
  - _Design rules: D5, D7–D8, D17_
  - _Boundary: internal/standardplugins/internal/pluginreg composition_
  - _Depends: 4.1, 3.2_
  - _Validation: go test ./internal/standardplugins/... ./internal/pluginreg/... ./internal/infra/runtimebundle/..._

- [ ] 4.3 Generalize route-kind ownership and derive route claims from frontend contributions
  - Keep normalized owner/method/path conflict validation.
  - Move concrete operation IDs to frontend-owned contributions/protocol packages and replace central closed route-kind additions with bounded opaque IDs.
  - Derive standard route-claim providers from frontend contributions.
  - Observable completion: current route collision tests pass and a synthetic new frontend route requires no `stdhttp/contract` enum edit.
  - _Requirements: 8.1–8.2, 8.7–8.8, 7.4_
  - _Design rules: D7–D8, D18_
  - _Boundary: stdhttp contract + frontend contribution metadata_
  - _Depends: 4.1_
  - _Validation: go test ./internal/stdhttp/contract/... ./internal/standardplugins/... ./internal/stdhttp/..._

- [ ] 4.4 Replace protocol-specific central diagnostics rows/switches with bounded generic contribution projectors
  - Characterize existing HTTP JSON first and retain compatibility serialization/versioning where needed.
  - Introduce common instance diagnostics plus bounded sanitized extension fields.
  - Move protocol-specific projection to contribution-owned side-effect-free projectors; remove OpenResponses-compatible/frontend selection switches from central composition.
  - Observable completion: operator information remains equivalent while a synthetic protocol adds diagnostics without a new core diagnostic DTO/switch.
  - _Requirements: 8.3–8.8, 7.4, 7.10_
  - _Design rules: D7–D10, D17_
  - _Boundary: internal/core/diag + standardplugins projection adapters_
  - _Depends: 4.1, 4.2_
  - _Validation: go test ./internal/core/diag/... ./internal/standardplugins/... ./internal/stdhttp/..._

## 5. Harden Canonical and Backend-Plugin Extension Boundaries

- [ ] 5.1 Audit protocol-named canonical fields with characterization tests before any migration
  - Trace all readers/writers of `PromptCacheKey`, reasoning Summary/Content/EncryptedContent presence, compaction encrypted content, and related OpenResponses/Codex carriers.
  - Classify each as core/shared semantic or adapter-only fidelity using Requirement 9 promotion rules.
  - Add characterization round-trips and projection/admission tests for every migration candidate.
  - Observable completion: classification/evidence is complete before changing public canonical shape.
  - _Requirements: 9.1–9.10, 1.9_
  - _Design rules: D9–D10, D14, D16_
  - _Boundary: canonical audit/tests_
  - _Depends: 2.1_
  - _Validation: go test ./pkg/lipapi/... ./internal/plugins/backends/openresponsescompat/... ./internal/plugins/frontends/openresponses/... ./connectors/codex/..._

- [ ] 5.2 Migrate only proven adapter-only canonical fidelity to bounded negotiated carriers
  - Reuse existing extension carriers where sufficient; add at most one generic presence-bearing semantic residual carrier if evidence proves a gap.
  - Keep shared/core-consumed reasoning/compaction semantics first-class.
  - Preserve source compatibility through aliases/transitional fields where practical and forbid raw request/response tunneling.
  - Observable completion: canonical first-class surface is smaller or no larger for adapter-only fidelity, with all characterization tests green.
  - _Requirements: 9.2–9.9, 1.9_
  - _Design rules: D9–D10, D17_
  - _Boundary: pkg/lipapi + adapter conversions only_
  - _Depends: 5.1_
  - _Validation: go test ./pkg/lipapi/... ./internal/core/... ./internal/plugins/frontends/... ./internal/plugins/backends/..._

- [ ] 5.3 Add semantic backend-plugin carrier/negotiation support without breaking v1.3
  - First add RED v1.0–v1.3 compatibility and unknown-carrier tests.
  - If required by 5.2, add one protocol-neutral semantic extension feature/carrier with strict bounds and exact requirement matching.
  - Preserve `exact_openresponses_fields` as legacy compatibility vocabulary; bridge without dual authority or silent loss.
  - Observable completion: current connectors negotiate exactly as before unless opting into the additive generic semantic feature.
  - _Requirements: 10.1–10.10, 1.2, 1.9_
  - _Design rules: D10–D12_
  - _Boundary: api/backendplugin/v1 + pkg/lipsdk/backendplugin + host adapter_
  - _Depends: 5.2, 2.4_
  - _Validation: go test ./pkg/lipsdk/backendplugin/... ./internal/infra/backendplugins/adapter/... ./tools/backendplugin/..._

- [ ] 5.4 Enforce future canonical/ABI promotion policy in architecture/docs
  - Add architecture tests for new protocol-named backend-plugin feature/proto additions outside the legacy allowlist.
  - Document the canonical promotion checklist and semantic-ABI rule with positive/negative examples.
  - Observable completion: a synthetic protocol-named ABI feature fails architecture tests while a semantic carrier extension passes.
  - _Requirements: 9.10, 10.3, 10.8, 10.11, 13.8–13.10_
  - _Design rules: D9–D12, D18_
  - _Boundary: archtest + docs_
  - _Depends: 5.3_
  - _Validation: go test ./internal/archtest/... && make docs-check_

## 6. Converge Continuation Ownership and Cut Over Conformance

- [ ] 6.1 Collapse continuation store/recorder mirrors to one authority
  - Run parity characterization against SDK/core copies first.
  - Retain `pkg/lipsdk/continuation` as default protocol-neutral contract/utility authority, retain durable infra adapters, and reduce core to orchestration/delegation.
  - Delete duplicate mutable state machines and add mirror regression guards.
  - Observable completion: one `MemoryStore` and one `StreamRecorder` authority remain with unchanged security/lifecycle behavior.
  - _Requirements: 11.1–11.9, 1.5_
  - _Design rules: D13, D17_
  - _Boundary: pkg/lipsdk/continuation + internal/core/continuation + internal/infra/continuation_
  - _Depends: 1.4_
  - _Validation: go test ./pkg/lipsdk/continuation/... ./internal/core/continuation/... ./internal/infra/continuation/..._

- [ ] 6.2 Build the bounded real-stack sentinel and prove provider-count independence
  - Select representative built-in, compatible-family, connector-host, stateful frontend, and negative admission paths with explicit `Protects` rationale.
  - Add bound tests and verify 1,000 additional profiles in existing families do not change sentinel count.
  - Observable completion: sentinel catches deliberately broken composition wiring but is independent of provider population.
  - _Requirements: 5.5–5.9, 2.6–2.9, 12.8_
  - _Design rules: D2, D15–D16_
  - _Boundary: integration/conformance sentinel tests_
  - _Depends: 2.2–2.4, 3.3, 4.2_
  - _Validation: go test -tags=precommit,integration ./internal/testkit/contract/... ./internal/integration/..._

- [ ] 6.3 Dual-run legacy matrix and TCK model with feature traceability/mutation proof
  - Map every current release-critical matrix feature to frontend/core/backend/profile/protocol/sentinel ownership.
  - Run both systems at the same implementation head.
  - Inject representative decode, projector, false-capability, connector-field-loss, and composition faults and prove the new owner suite catches each.
  - Observable completion: no required feature has matrix-only ownership.
  - _Requirements: 12.1–12.3, 12.9, 5.10_
  - _Design rules: D14–D16_
  - _Boundary: migration evidence/tests_
  - _Depends: 2.1–2.4, 6.2_
  - _Validation: make parity-checks && go test ./internal/testkit/contract/..._

- [ ] 6.4 Retire Cartesian completeness and delete obsolete evidence scaffolding
  - Switch parity/release correctness to TCK certifications + protocol-owned suites + bounded sentinel.
  - Remove authoritative `AllCells` completeness, OpenResponses row/column/general-cell feature evidence used only by Cartesian proof, and pairwise metadata.
  - Preserve independent emulators/compliance and reusable scenarios.
  - Enforce ≥80% deletion of baseline Cartesian-only non-generated Go lines and no net growth in reviewed affected shared surfaces.
  - Observable completion: no mandatory release path constructs all frontend×backend pairs; deletion gates pass.
  - _Requirements: 2.1, 5.10, 12.3–12.10_
  - _Design rules: D2, D14–D17_
  - _Boundary: internal/testkit/conformance, Makefile/workflows as needed_
  - _Depends: 6.3_
  - _Validation: make test && make parity-checks && make quality-checks_

## 7. Add Change-Surface Ratchets, Documentation, and Release Evidence

- [ ] 7.1 Implement extension change-surface reporting and hard profile-only boundary checks
  - Classify diffs into extension-owned, provider-profile, shared composition, canonical, core routing/runtime, ABI, generated, tests/reference, and docs/spec categories.
  - Add fixture tests showing generated/test breadth does not equal architectural coupling.
  - Enforce zero canonical/core/frontend/ABI/shared-registry edits for provider-profile-only fixture additions.
  - Observable completion: report explains blast radius and profile-only contract fails on synthetic shared-boundary edits.
  - _Requirements: 13.5–13.8, 2.2–2.3_
  - _Design rules: D5, D7, D18_
  - _Boundary: tooling/internal/archtest_
  - _Depends: 4.2, 6.4_
  - _Validation: go test ./internal/archtest/... ./tools/... && make quality-checks_

- [ ] 7.2 Consolidate architecture rules and remove protocol-specific guard duplication where generic rules suffice
  - Replace OpenResponses-only import/registry guards with zone/contribution/ABI rules where semantics are generic.
  - Retain protocol-specific guards only for real wire/provenance/emulator-independence constraints.
  - Observable completion: architecture suite becomes smaller or equal while catching representative violations for arbitrary synthetic protocols/providers.
  - _Requirements: 13.1–13.4, 12.7_
  - _Design rules: D7–D12, D17–D18_
  - _Boundary: internal/archtest_
  - _Depends: 4.2–4.4, 5.4, 6.4_
  - _Validation: go test ./internal/archtest/..._

- [ ] 7.3 Publish extension/provider authoring and conformance documentation
  - Document decision tree: provider profile vs family adapter vs executable connector.
  - Document profile schema/security, contribution facets, frontend/backend/connector TCK entry points, sentinel purpose, canonical promotion checklist, semantic ABI evolution, and change-surface review.
  - Remove docs that describe 5×9 Cartesian completeness as permanent architecture.
  - Observable completion: a provider author can add a profile/family/connector without discovering hidden central registries.
  - _Requirements: 6.5, 9.10, 13.9–13.10_
  - _Design rules: D5–D12, D15, D18_
  - _Boundary: docs/steering updates where durable_
  - _Depends: 7.2_
  - _Validation: make docs-check_

- [ ] 7.4 Run final certification, scale, race/fuzz, shrinkage, and release gates
  - Run frontend/core/backend/connector/profile TCK certifications, bounded sentinel, protocol-specific compliance, synthetic 1,000-profile scale, architecture/change-surface report, continuation security/race, relevant fuzz, and full repository quality gates.
  - Record baseline SHA, certification subjects/scenarios, legacy surface deletion %, affected-surface line delta, synthetic scale counts, wall-clock evidence, and any environmental limitations without claiming skipped evidence.
  - Observable completion: all deterministic gates pass at exact implementation head and spec tasks can be closed with auditable evidence.
  - _Requirements: 1.1–1.10, 2.1–2.10, 3.1–3.10, 4.1–4.10, 5.1–5.10, 6.1–6.12, 7.1–7.10, 8.1–8.8, 9.1–9.10, 10.1–10.11, 11.1–11.9, 12.1–12.10, 13.1–13.12_
  - _Design rules: D1–D18_
  - _Boundary: repository release evidence_
  - _Depends: 7.1–7.3_
  - _Validation: make quality-checks && make test && make qa; make test-race where supported; targeted provider-profile/semantic-carrier fuzz campaigns_
