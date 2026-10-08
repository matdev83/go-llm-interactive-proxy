# Implementation Plan

This plan targets **current `main` as audited on 2026-10-08 at `79a608632a1583b685f938103685ec34a78d5fa9`**, after both core-feature-ownership closure and bulk-provider expansion were merged. Written for an implementation agent: follow this updated plan, recheck changed code on your actual branch, and never rebuild the completed profile lifecycle.

## Mandatory Execution Rules

1. **Completed predecessor / verify-only gate:** `core-feature-ownership-full-closure` was implemented by #598, archived by #599, remediated by #600 and lives at `.kiro/specs/archive/core-feature-ownership-full-closure/`. Verify its live core-admission ratchets on the implementation branch; do not wait for or redo its work.
2. Rebranding implementation is tracked by #641 (#429 was planning). Use actual package names on the implementation branch if #641 has landed; do not proactively rename unrelated symbols or alter the frozen unimplemented `aipca1_`/HMAC domains.
3. Do not create `internal/core/cacheaffinity`, a cache-specific executor stage/field, a cache-specific `SecurityRuntime` field, `execbackend.Backend.ResolveDownstreamCacheAffinity`, a cache-specific `ProcessServices` field, or a new cache-affinity extension plane.
4. Reuse existing `PlaneAttemptTransforms`; cache-affinity derivation/policy/telemetry lives in `internal/plugins/features/downstreamcacheaffinity` and standard construction lives in `internal/standardplugins/featurehost`.
5. The only permitted new core-facing capability is a **generic bounded immutable backend-feature ID list**, and only if Task 1 proves no equivalent post-closure carrier exists.
6. Provider wire names live only in provider profile/backend/connector code. The feature algorithm receives only generic backend features and stable backend prefixes.
7. Generic synthesis input is admitted `AuthoritativeSessionID` only. Never use client hint, A-leg, principal/user/IP/request ID, workspace metadata or SafeMetadata.
8. Generated value is exactly `aipca1_` + full SHA-256 raw-base64url digest = 50 characters. Never truncate.
9. HMAC domains are exactly `aiproxer/downstream-cache-affinity/key/v1\x00` and `aiproxer/downstream-cache-affinity/value/v1\x00`.
10. The feature never receives the secure-session fingerprint root. It receives only a 32-byte domain-derived subkey from a generic featurehost composition capability.
11. Direct OpenAI Responses PCK forwarding must be repaired before its synthesis path is certified.
12. **Do not rebuild provider-profile lifecycle:** #619/#622 already delivered marker-aware expansion (`ExpandProviderProfileRowsWithCatalog`, `wrapCompatibleLifecycle`, `buildProviderProfileBackendWithNode`). Verify it and extend only its compiled-profile data/projection; prepared compatible-family row kind changes are intentional.
13. No new backend-plugin value protobuf field/protocol minor. Add negotiated `downstream_cache_affinity_v1` at semantic-extension minimum minor 6 (current host minor 9); update protocol feature-minor registry **and both host negotiation offer feature lists**.
14. OpenRouter uses JSON body `session_id` only; explicit existing `openrouter.session_id` wins over effective PCK; no `x-session-id`. Do not let the later generic extra-body extension loop silently overwrite `session_id`.
15. TDD/characterization first for every brownfield seam. No unrelated cleanup/refactor.
16. An enabled `PlaneAttemptTransforms` occupant is canonical-required for the large-payload fast path. Cache-affinity must be **explicit generation opt-in**, default absent/disabled with zero plane occupancy; enabled calls use canonical fallback when the wire assessor declines. Never bypass that safety gate.
17. The generic domain-key capability takes a **NUL-free domain label**. Its secure-session owner appends the single terminal `\x00` byte inside HMAC input; do not pass a literal NUL to a validator that rejects controls. The frozen HMAC message remains byte-for-byte unchanged.
18. If a STOP condition fires, stop that wave and repair the SDD; do not invent another framework or core exception.

## Target File Map

Use these verified October 8 paths or the actual mechanically renamed equivalents after #641:

| Concern | Target |
|---|---|
| Generic backend feature IDs | `pkg/lipsdk/backendfeature/` (only if no equivalent exists) |
| Generic executor backend feature value | `internal/core/execbackend/backend.go` |
| Generic attempt metadata projection | `pkg/lipsdk/request/attempt_transform.go`, `internal/core/runtime/executor_attempt_transform.go` |
| Feature implementation | `internal/plugins/features/downstreamcacheaffinity/` |
| Standard composition / enablement | `internal/standardplugins/featurehost/cacheaffinity.go`, `featurehost/inputs.go`, `featurehost/generation.go`, existing feature-factory registration |
| Generic secure-root domain-key adapter | `internal/infra/runtimebundle/secure_session.go`, `build_persistence.go`, `process_services.go` (root never exported) |
| Direct OpenAI PCK | `internal/plugins/backends/openairesponses/invoke.go`, constructor |
| Completed provider-profile lifecycle (reuse only) | `internal/standardplugins/provider_profile_binding.go` (`ExpandProviderProfileRowsWithCatalog`, `wrapCompatibleLifecycle`, `buildProviderProfileBackendWithNode`); `standard_contributions.go` |
| Provider schema/catalog | `internal/providerprofiles/schema.go`, `catalog.json` |
| Profile-compatible projection | `internal/standardplugins/provider_profile_binding.go`, `internal/plugins/backends/openaicompat/*` |
| OpenRouter carrier | `connectors/openrouter/internal/service/body.go`, `service.go` |
| Backend-plugin feature | `pkg/lipsdk/backendplugin/bounds.go`, `protocol.go`, `host/session.go` (two offer lists), `internal/infra/backendplugins/adapter/backend.go` |
| Feature telemetry | feature observer + `internal/standardplugins/featurehost`/existing infra metrics adapter |
| Reusable TCK | `internal/testkit/contract/cacheaffinity/`; wire parity in `internal/core/largebody`/`internal/infra/runtimebundle` |

---

# 0. Confirm Completed Predecessors and Rebaseline

- [ ] 0.1 Verify archived ownership closure against the live tree
  - Read `.kiro/specs/archive/core-feature-ownership-full-closure/` and `final-ownership-census.md`; cite implementation #598, archive #599 and remediation #600, but independently test current main rather than trusting issue text.
  - Record implementation-base SHA, existing core-admission manifest/ratchets, and standard `featurehost` composition owner in PR evidence.
  - Assert no new optional cache-specific `ProcessServices`/`runtime.Executor`/`SecurityRuntime` fields; only the standard featurehost handle may cross the generic boundary.
  - If the original closure contract is actually broken, STOP and repair that pre-existing architectural regression independently; do not reimplement completed closure as this feature.
  - _Boundary: architecture rebaseline_
  - _Validation: predecessor arch tests, `make arch-report`, focused featurehost tests_

- [ ] 0.2 Re-inventory current implementation owners and already-completed work
  - Pin `PlaneAttemptTransforms`, `candidateAttemptMeta`, `featurehost.GenerationInput.Registrations`, `buildSecureSessionRuntime` fingerprint root, `process_services.go` featurehost construction, direct OpenAI Responses serializer, OpenRouter body loop, backendplugin dual host offers and protocol feature-minor list.
  - Prove `execbackend.Backend` and `request.AttemptMeta` still lack an equivalent bounded generic backend-feature carrier before adding Task 2.
  - Identify compiled-profile marker-aware path: `ExpandProviderProfileRowsWithCatalog` -> prepared family row with reserved marker -> `wrapCompatibleLifecycle` -> `buildProviderProfileBackendWithNode`.
  - Capture catalog inventory: existing `fireworks`/`mistral`/`xai`; missing `xai-responses`/`runinfra` on audited main. Recheck before editing data.
  - Recheck provider docs for the exact proposed wire carriers and bounds; do not assume external API compatibility just because catalog rows exist.
  - _Boundary: brownfield inventory_
  - _Depends: 0.1_
  - _Validation: repository symbol/import scan, targeted provider/profile/host tests_

# 1. RED Characterization Before Production Changes

- [ ] 1.1 Characterize candidate-transform timing and PCK ownership
  - Add focused tests proving the current attempt-transform stage receives: authoritative `SessionView`, selected backend ID, stable backend prefixes, model and independent call copy for parallel candidates.
  - Inventory every production writer of `PromptCacheKey`, `SemanticPromptCacheKeyType`, and any helper that mutates the PCK semantic.
  - Prove **no stage after `PlaneAttemptTransforms` and before backend serialization currently creates/replaces PCK**. Existing serializers may read/project it; that is allowed.
  - Prove `AdaptCallForCandidate` preserves an already-set PCK for representative capable OpenAI Responses and compatible-family candidates.
  - Prove the final session scrub removes all raw session fields while preserving PCK.
  - Add an AST/architecture allowlist ratchet that fails on a new post-attempt-transform production PCK writer outside known serializers/adapters.
  - **STOP condition:** if a legitimate later PCK writer exists, stop and update this SDD. Do not add a cache-specific late executor hook.
  - _Requirements: 2,5_
  - _Boundary: core generic extension timing / tests only_
  - _Validation: focused `internal/core/runtime`, `internal/archtest`, `pkg/lipapi` tests_

- [ ] 1.2 Characterize backend feature negotiation/metadata
  - Inventory existing post-closure generic backend capability/feature carriers across `execbackend.Backend`, backend-plugin negotiation, candidate metadata and provider-profile construction.
  - Prove backend-plugin negotiation already has bounded feature names and generation-stable results.
  - Add RED fixture showing a candidate attempt transform currently cannot distinguish a feature-negotiated executable backend from a lacking-feature peer unless an equivalent generic carrier already exists.
  - Record the smallest generic metadata change needed; no callback/value maps.
  - _Requirements: 2,6,8_
  - _Boundary: generic backend capability metadata_
  - _Depends: 0.2_

- [ ] 1.3 Characterize direct OpenAI explicit PCK gap
  - Extend direct OpenAI Responses serializer tests with explicit legacy PCK, semantic PCK, equal aliases, conflicting aliases, empty, length 64 and length 65.
  - RED must show explicit PCK is not currently serialized if that remains true on the implementation base.
  - Add a control proving direct OpenAI Chat is not implicitly enabled by this SDD.
  - _Requirements: 7.1,11.1_
  - _Boundary: OpenAI Responses backend_
  - _Depends: 0.2_

- [ ] 1.4 Certify the existing marker-aware `provider-profile` production path
  - Configure `kind: provider-profile`, `config.profile: <fixture>` and drive through `PrepareProviderProfiles`, the real standard compatible-family `wrapCompatibleLifecycle`, registry and candidate build.
  - Require the *operator source row* to stay untouched, while the prepared clone intentionally changes to a compatible-family kind carrying a reserved YAML anchor/head-comment marker.
  - Pin a compiled-only disabled capability, a static safe header and representative quirk/dialect; prove the marker-aware builder restores them all and an arbitrary unmarked custom-compatible row remains unaffected.
  - Exercise forged/reserved marker, wrong family, unknown profile, and invalid catalog-handle refusal. If lifecycle semantics are lost, repair the existing wrapper/compiled owner only and STOP on attempts to add a second profile factory.
  - _Requirements: 6.8,6.10,7.10_
  - _Boundary: completed provider profile production lifecycle (verification)_
  - _Depends: 0.2_

- [ ] 1.5 Capture baseline cost and ownership evidence
  - Record non-test core LOC/budget/manifest status from predecessor before this feature.
  - Benchmark an empty/no-op AttemptTransform baseline and current PCK serializers with `-benchmem`.
  - Record that no cache-affinity-specific production package/symbol exists in core; baseline with large-payload fast path enabled both with empty plane occupancy and with a controlled occupied canonical-required attempt-transform plane.
  - Final Task 12 must prove core feature-specific LOC remains unchanged except for the generic backend-feature metadata seam if Task 2 is required.
  - _Requirements: 2,9,11_
  - _Boundary: performance/architecture evidence_
  - _Depends: 0.2_

- [ ] 1.6 RED characterization of large-payload wire eligibility and feature activation
  - With `server.large_payload_fast_path.enabled`, prove `PlaneAttemptTransforms` is `RequestBodyCanonicalRequired`; an occupied transform makes the wire authority gate decline, while an empty plane preserves existing eligibility.
  - Pin one canonical candidate and a wire-compatible control with feature absent/disabled. Any feature-enabled candidate must not open via raw wire while skipping HMAC synthesis.
  - Define the smallest existing-registry/featurehost registration path for generation-scoped opt-in and validate disable/reload/parallel-generation no-occupant behavior.
  - _Requirements: 2.11,5.9,9.6-9.7_
  - _Depends: 0.2_
  - _Validation: `internal/core/largebody`, featurehost/standard runtime integration tests_

---

# 2. Add/Re-use Generic Immutable Backend Feature Metadata

> Skip new API creation if Task 1.2 finds an equivalent post-closure carrier. Adapt the following tests/uses to that existing carrier.

- [ ] 2.1 Add bounded `backendfeature.ID` value contract
  - Default package: `pkg/lipsdk/backendfeature`.
  - Add `type ID string`, `MaxIDBytes = 128`, `DownstreamCacheAffinityV1 = "downstream_cache_affinity_v1"`, `Validate`, `Normalize`, `Contains`.
  - Validation: non-empty bounded token-like ASCII; reject whitespace/control/NUL; normalization validates, deduplicates, stable-sorts and defensively copies.
  - No registries, callbacks, arbitrary values, service lookup, configuration decoding or provider names.
  - _Requirements: 2.6-2.7,6.2_
  - _Depends: 1.2_
  - _Validation: package unit/fuzz tests_

- [ ] 2.2 Add one generic immutable feature list to executor backend value
  - Add `Features []backendfeature.ID` (or reuse equivalent) to `execbackend.Backend`.
  - Add one clone/normalize helper consistent with existing `BackendPrefixes` helpers.
  - Backend construction must publish normalized immutable features; runtime must not mutate after publication.
  - Do not add `ResolveFeatures`, map values, cache-specific fields or feature callbacks.
  - _Requirements: 2.6-2.7,6.2_
  - _Depends: 2.1_
  - _Validation: `execbackend` tests + architecture guard against callback/value-map growth_

- [ ] 2.3 Project generic backend features into `request.AttemptMeta`
  - Add `BackendFeatures []backendfeature.ID` to `request.AttemptMeta` (or equivalent existing metadata).
  - `candidateAttemptMeta` defensively copies the selected backend's feature list.
  - Add tests for empty, one feature, multiple normalized features and caller mutation isolation.
  - No cache-specific branch in runtime.
  - _Requirements: 2.6-2.7,6.2_
  - _Depends: 2.2_
  - _Validation: request SDK + runtime attempt-transform tests_

---

# 3. Implement Feature-Owned Derivation and Attempt Transform

- [ ] 3.1 Add exact feature derivation implementation
  - Create `internal/plugins/features/downstreamcacheaffinity/derive.go`.
  - Constants: `GeneratedPrefix = "aipca1_"`, `GeneratedLength = 50`, `MaxNamespaceBytes = 128`.
  - `Deriver` stores only `[32]byte` subkey supplied by standard featurehost.
  - Value formula exactly: HMAC-SHA256(subkey, `"aiproxer/downstream-cache-affinity/value/v1\x00" || namespace || "\x00" || authoritative_session_id`), then prefix + full raw-url-base64 digest.
  - Add hard-coded deterministic vector, namespace/session/subkey separation, safe alphabet, exact length, empty/control/oversized validation.
  - No provider wire names, core imports or root-key knowledge.
  - _Requirements: 3,4,9_
  - _Depends: 0.2_
  - _Validation: feature derive tests + fuzz invalid bounds_

- [ ] 3.2 Add feature-owned observer and fill-only candidate transform
  - Create `observer.go`, `transform.go`.
  - Frozen ID `downstream-cache-affinity`; `TransformOrder = 1_000_000`; failure mode fail-closed for canonical PCK conflicts/internal impossible errors. Confirm any newly added standard transforms do not sort after it.
  - Algorithm exactly from design: existing PCK -> preserve; missing generic backend feature -> unsupported; invalid/no prefix -> no synthesis; nil deriver/no authoritative session -> disabled; otherwise derive from `BackendPrefixes[0]` + `Session.AuthoritativeSessionID` and set only `call.PromptCacheKey`.
  - Never read `ClientSessionHint`, A-leg, principal/scope/workspace/trace/model for derivation.
  - Unsupported/disabled/invalid backend support does not exclude candidate; PCK alias conflict returns error.
  - Observer event contains only bounded source/outcome/backend ID.
  - _Requirements: 1,2,3,4,5,9,10_
  - _Depends: 1.1,2.3,3.1_
  - _Validation: feature transform table + race-safe observer tests_

- [ ] 3.3 Build an ordinary `FeatureBundle`
  - Create `bundle.go` with exactly one runtime transform for an **enabled** generation. Use the existing standard feature registration/factory convention for a minimal enabled config if required; do not duplicate contribution between ordinary registration and `featurehost` binding.
  - No new plane, lifecycle, goroutine, state store, generic service registry or cache-specific core config field. A small typed feature config decoder is allowed only to validate opt-in, not to create a second provider framework.
  - Prove absent/disabled/no-deriver composition adds **zero** cache-affinity attempt-transform occupants (especially on wire-eligible requests), not a disabled no-op transform.
  - Add bundle plane-parity/typed-nil tests per repository conventions.
  - _Requirements: 2.1-2.4,11.2_
  - _Depends: 3.2_
  - _Validation: feature bundle tests + existing plane parity checks_

---

# 4. Compose the Feature Through Standard `featurehost`

- [ ] 4.1 Derive a generic feature subkey from the existing secure-session root
  - In `internal/infra/runtimebundle/secure_session.go:buildSecureSessionRuntime` retain only a narrow generic domain-key derivation capability over the already-resolved fingerprint root `fp`; persistence builds it before `featurehost.NewProcess` in `process_services.go`.
  - Add/reuse `featurehost.ProcessInput.DomainKeyDeriver func(domainLabel string) ([32]byte,error)` (or an equivalent narrow generic function). Validate a **NUL-free** ASCII domain label <=128 bytes, reject empty/embedded controls, then HMAC-SHA256 over `domainLabel || "\x00"` in the secure-session owner. This preserves frozen on-wire derivation while avoiding a contradictory validate-NUL call.
  - Pass this one generic process capability through existing build-persistence/process-services/featurehost ownership, not a cache-specific `ProcessServices` field. The feature/featurehost never receive root bytes.
  - Test exact frozen HMAC byte vector, missing capability, process-local ephemeral vs durable key lifetime, safe errors, and key isolation. No new process secret.
  - _Requirements: 2,4.4-4.7,9_
  - _Depends: 0.1,1.5_
  - _Validation: secure-session owner/featurehost key-domain tests_

- [ ] 4.2 Add standard featurehost cache-affinity adapter
  - Create `internal/standardplugins/featurehost/cacheaffinity.go` (or post-rebrand equivalent).
  - Ask the generic key capability exactly once for **NUL-free label** `aiproxer/downstream-cache-affinity/key/v1` during process/standard-feature construction; the key owner appends the single terminal NUL inside the HMAC input.
  - Construct `downstreamcacheaffinity.Deriver` from returned subkey.
  - Construct safe feature observer backed by the existing generic `featurehost.ProcessInput.MetricsRegistry`, and merge one runtime-bound transform into standard generation composition **only for an explicitly enabled registration** in `featurehost.GenerationInput.Registrations`. Use existing feature factory/registration validation rather than an unconditional process-wide transform.
  - Missing generic key capability, absent/disabled registration, and disabled/reloaded generation => no synthesis transform; do not fail base proxy startup merely for this optimization.
  - No cache-specific field in `ProcessServices`/Executor/SecurityRuntime.
  - _Requirements: 2,4,10_
  - _Depends: 3.3,4.1_
  - _Validation: featurehost process/generation tests, absent/disabled opt-in, overlapping/reloaded generation test, and no double contribution_

- [ ] 4.3 Prove standard integration and raw-session scrub
  - End-to-end standard distribution test: authoritative session + capable fake backend -> exact deterministic generated PCK reaches backend `Open`; every session field is empty there.
  - Existing explicit PCK -> unchanged.
  - Client hint without authoritative session -> no generated PCK.
  - Two backend prefixes -> namespace follows each selected backend; parallel copies do not share mutable state.
  - With wire fast path enabled, absent/disabled feature preserves wire eligibility; enabled generation correctly declines the wire path and invokes canonical synthesis. Re-enable after reload and verify a single transform, not stale/double occupancy.
  - _Requirements: 1,3,5,9_
  - _Depends: 4.2_
  - _Validation: featurehost/runtime integration tests under race_

---

# 5. Repair Direct OpenAI Responses PCK and Enable Generic Feature

- [ ] 5.1 Serialize existing `PromptCacheKey` in direct OpenAI Responses
  - Edit `internal/plugins/backends/openairesponses/invoke.go` (or renamed equivalent).
  - Resolve effective PCK once at serializer boundary; conflict error; empty omit; >64 reject before provider call; <=64 assign typed `ResponseNewParams.PromptCacheKey`.
  - Do not use untyped JSON override.
  - No other request-shape changes.
  - _Requirements: 7.1,11.1_
  - _Depends: 1.3_
  - _GREEN: direct OpenAI PCK RED tests pass_

- [ ] 5.2 Advertise generic backend feature on direct Responses only
  - Add `backendfeature.DownstreamCacheAffinityV1` to direct OpenAI Responses backend immutable features.
  - Keep direct OpenAI Chat control disabled unless already independently supported and covered by frozen provider evidence (this SDD does not add it).
  - Add feature-transform integration test using the real constructed backend prefix.
  - _Requirements: 6,7.1_
  - _Depends: 2.2,5.1_

---

# 6. Reuse and Certify the Existing `provider-profile` Lifecycle

- [ ] 6.1 Verify production preparation and reserved marker ownership
  - In `provider_profile_binding.go`, retain `ExpandProviderProfileRowsWithCatalog` and its existing `lip_profile_` anchor / `lip:provider-profile:` comment handoff; verify prepared rows are compatible-family clones, **not** retained source `provider-profile` kinds.
  - Confirm original config rows are immutable and arbitrary unmarked custom-compatible configs take the existing plain family lifecycle.
  - Do NOT create `LifecycleProviderProfile`, a second backend kind contribution, or a duplicate profile catalog; these would regress the completed #619/#622 contract.
  - _Requirements: 6.8,6.10_
  - _Depends: 1.4_

- [ ] 6.2 Verify the existing `wrapCompatibleLifecycle` registration and compiled semantics
  - Prove the four registered compatible families in `standard_contributions.go` wrap their existing lifecycle only once and resolve marker to `CompiledProfile` before constructing a backend through `buildProviderProfileBackendWithNode`.
  - Pin compiled disabled capabilities, headers, quirks, dialects, prefixes and live candidate construction. Repair **only** the existing owner if any already-supported semantic is lost.
  - _Depends: 6.1_

- [ ] 6.3 Guard marker misuse and source-config immutability
  - Add regression tests for forged/reserved marker on custom-compatible rows, wrong-family marker, unknown/missing catalog profile, round-trip anchor/comment, and valid test-catalog handle.
  - The prepared-row kind/config rewrite is **allowed** only on the clone and only with a marker that the wrapper validates. Unknown markers must not silently degrade to generic backend semantics.
  - _Depends: 6.2_

- [ ] 6.4 GREEN certification of current live production path
  - Run Task 1.4 fixture through actual registry/lifecycle/candidate build; prove source row remains `provider-profile`, prepared row carries validated marker, backend retains compiled-only semantics, and no second provider-profile factory exists.
  - This is verification and cache-projection extension readiness, **not** a request to replace the already-merged lifecycle.
  - _Validation: standardplugins provider-profile + runtimebundle candidate tests_
  - _Depends: 6.3_

# 7. Add Typed `cache_affinity` Profile Schema

- [ ] 7.1 Add exact schema/value types
  - Add `CacheAffinityTransport`, `CacheAffinityProjection`, `CacheAffinity` and `Profile.CacheAffinity` exactly as design.
  - Add local `MinCacheAffinityValueLength = 50` in `internal/providerprofiles`; do not import the feature package.
  - No schema version bump, arbitrary transform DSL or provider-specific Go type.
  - _Requirements: 6.4-6.9_
  - _Depends: 6.4_

- [ ] 7.2 Add strict validation
  - Disabled strict-zero; enabled transport only `json_field|http_header`; safe required wire name; JSON-field regex; max >=50 and <=MaxStringBytes; family/flavor exclusivity; synthesis implies enabled.
  - Add negative fixtures for every invalid dimension.
  - Add architecture cross-test `providerprofiles.MinCacheAffinityValueLength == downstreamcacheaffinity.GeneratedLength` without production dependency.
  - _Depends: 7.1_

---

# 8. Thread Profile Projection Through Compatible Family Builders

- [ ] 8.1 Preserve complete cache-affinity projection in profile-aware build
  - Thread the validated selected flavor projection from `CompiledProfile` through `wrapCompatibleLifecycle` -> `buildProviderProfileBackendWithNode` -> existing profile-family builder to the corresponding OpenAI-compatible serializer.
  - Never reconstruct typed affinity policy solely from the prepared generic compatible YAML node; preserve existing compiled capability/header/quirk/dialect behavior.
  - `Enabled && AllowProxySynthesis` appends `backendfeature.DownstreamCacheAffinityV1` to the constructed backend features.
  - Profile/backend prefix remains synthesis namespace; no provider-specific value in feature code.
  - _Requirements: 6,7_
  - _Depends: 2.2,7.2_

- [ ] 8.2 Project effective PCK only in the selected serializer
  - Empty PCK -> no option; over max -> pre-output error.
  - JSON -> `option.WithJSONSet(WireName,value)`; header -> `option.WithHeader(WireName,value)`.
  - Arbitrary custom-compatible rows get no projection/feature flag.
  - Add Chat header, Chat JSON, Responses JSON and disabled/custom negative tests.
  - _Depends: 8.1_

- [ ] 8.3 Add real production-path cache-affinity assertion
  - Extend Task 6.4 fixture with typed profile cache-affinity projection and run through prepared marker -> compatible-family wrapper -> registry/lifecycle/candidate construction.
  - Prove generic backend feature is present only for compiled profile enabling synthesis, real serializer projects PCK, source config is immutable, and arbitrary custom-compatible row never inherits typed cache affinity.
  - Direct `BuildProviderProfileBackend` unit alone is not acceptance evidence.
  - _Depends: 8.2_

---

# 9. Add/Augment Frozen Initial Profile Rows

- [ ] 9.1 Add/augment `fireworks`, `xai`, `xai-responses`, `mistral`, `runinfra`
  - Use exact family/base/env/projection matrix in design/research.
  - **Known current catalog state:** `fireworks`, `mistral`, `xai` exist; augment in place. `xai-responses`, `runinfra` are missing; add them. Recheck current catalog before editing. Preserve strict disabled capabilities, static inventory, endpoints and auth.
  - Do not broaden unrelated model capabilities/tokenizers.
  - Do not create dedicated provider Go packages.
  - _Requirements: 7.2-7.6,7.9-7.10_
  - _Depends: 8.3_

- [ ] 9.2 Lock negative and catalog population behavior
  - Direct Anthropic, direct Gemini, unknown/custom compatible, and profile without projection remain synthesis-disabled.
  - Add expected row/projection table to catalog tests with exact endpoint/env/family/max/carrier/synthesis values.
  - _Depends: 9.1_

---

# 10. OpenRouter and Executable-Backend Feature Negotiation

- [ ] 10.1 Add backend-plugin feature ID without value DTO/minor bump
  - Add `FeatureDownstreamCacheAffinity = "downstream_cache_affinity_v1"` at semantic-extension minimum minor 6. Current host minor is 9 (`ProtocolMinorAccountingEvidenceV2`), not 6.
  - Add centralized feature-minor metadata in `pkg/lipsdk/backendplugin/protocol.go`; advertise the optional feature in **both** `pkg/lipsdk/backendplugin/host/session.go` lists (`ProtocolOffer.Features` and actual `NegotiateRequest.HostFeatures`) with explicit parity/legacy tests.
  - No new invocation field/protobuf message and no protocol minor increment.
  - _Requirements: 8_
  - _Depends: 2.1_

- [ ] 10.2 Map negotiated plugin feature to generic backend feature metadata
  - In backend-plugin adapter, only successful new feature negotiation **and a usable existing PCK carrier under the negotiated minor/feature set** and at least one stable route/backend prefix => append generic `backendfeature.DownstreamCacheAffinityV1`.
  - No feature/no carrier/no prefix/old peer => absent. Never mistake plugin advertisement alone for negotiated enablement.
  - Do not add a cache-specific `execbackend.Backend` resolver.
  - _Depends: 10.1,2.2_

- [ ] 10.3 Implement OpenRouter body precedence and advertise feature
  - `Describe`: existing required features + new cache-affinity feature.
  - Body: explicit existing `openrouter.session_id` > `call.PromptCacheKeyValue()` > omit; enforce after/before the `extraBodyPrefix` loop so a generic extra-body `session_id` cannot silently overwrite it.
  - Max256; JSON `session_id` only; no `x-session-id`; validate conflicts, invalid type/empty and oversize before upstream I/O.
  - Add explicit/generated/absent/conflict/extra-body-collision/oversize tests.
  - _Depends: 10.2_

---

# 11. Feature Telemetry, TCK and Cross-Authority Regressions

- [ ] 11.1 Wire feature-owned observer to existing metrics infrastructure
  - Keep source/outcome/backend only; bounded enums and existing bounded backend label policy.
  - No method on core runtime metrics interfaces.
  - Panic/failure in observer must not change request correctness according to repository safe-observer conventions.
  - _Requirements: 2.8,10_
  - _Depends: 4.2_

- [ ] 11.2 Add reusable cache-affinity TCK
  - Create/refresh `internal/testkit/contract/cacheaffinity/`.
  - Cover deterministic derivation, explicit precedence, capability gating, no-session, namespace separation, serializer bounds, connector negotiation (dual host offer parity), session scrub, compiled profile-marker lifecycle, wire eligibility/default-off/canonical fallback and unknown/custom negatives.
  - Offline only; no frontend×provider Cartesian suite.
  - _Depends: 5,8,10_

- [ ] 11.3 Prove residency/keep-warm/continuation separation
  - Generated PCK cannot populate promptcache TargetID/GenerationID/Handle/Timing.
  - Hint emission alone cannot arm keep-warm.
  - It cannot become previous-response ID, Codex turn state, ACP session, WebSocket continuation or transport connection identity.
  - Retry same namespace -> same value; failover different namespace -> different value; parallel arms independent.
  - _Requirements: 1,8,10_
  - _Depends: 11.2_

---

# 12. Performance, Architecture, QA and Closeout

- [ ] 12.1 Add permanent lean-core architecture ratchets
  - Reject: `internal/core/cacheaffinity`; cache-specific Executor/SecurityRuntime/ProcessServices/execbackend fields/callbacks; cache-specific new plane; direct runtimebundle concrete feature construction; feature imports of core/runtimebundle; providerprofiles import of feature; provider wire literals in generic core/feature policy; new plugin value DTO/minor; raw session backend wire; generated PCK residency/session/continuation use; duplicate provider-profile lifecycle or **loss of compiled semantics through the existing reserved-marker prepared-row rewrite**; disabled cache-affinity plane occupancy; wire-path bypass with active canonical transform.
  - Preserve predecessor core-admission manifest and budget. Generic backend feature metadata is explicitly classified as a generic extension mechanism, not cache ownership.
  - _Requirements: 2,11_
  - _Depends: all production tasks_

- [ ] 12.2 Benchmark hot paths
  - Bench feature transform: explicit PCK, generated, no backend feature, no authoritative session.
  - Bench direct OpenAI/profile serializers that consume PCK.
  - Assert no DB/network/filesystem/goroutine/timer and bounded allocations; subkey derivation absent from request benchmark.
  - Compare against Task 1.5 baseline on same host/toolchain where practical. Benchmark optional activation's wire fast-path eligibility impact and canonical fallback against disabled wire-eligible controls; document the tradeoff rather than masking it.
  - _Requirements: 9_
  - _Depends: 11_

- [ ] 12.3 Run full certification
  - Focused feature/profile/OpenAI/OpenRouter/backendplugin tests, disabled/enabled generation reload and current wire-fast-path parity/authority tests.
  - Generated/check/arch/parity/database gates required by repository.
  - Exact Linux race scopes required by predecessor/current QA.
  - `make quality-checks`, `make test`, `make arch-report` and current aggregate parity gates.
  - Optional live provider validation remains non-blocking and credential-gated.
  - _Depends: 12.1,12.2_

- [ ] 12.4 Regenerate ownership census and perform independent review
  - Regenerate post-feature core/feature ownership census using predecessor method.
  - Required result: cache-affinity derivation/policy/telemetry is feature-owned; no new `mixed`, `unknown`, deferred or temporary core rows; generic backend feature metadata is classified as a bounded reusable extension mechanism.
  - Search for TODOs/placeholders, legacy `lipca1_`/`go-lip/downstream-cache-affinity` identifiers, duplicate provider-profile repair paths, and cache-specific core fields.
  - Independent reviewer gives GO/NO-GO against requirements/design/tasks and current main.
  - _Depends: 12.3_

- [ ] 12.5 Merged-main certification and archive
  - Re-run required gates on merged `main`.
  - Record exact merge SHA, **reuse of completed #619/#622 provider-profile owner** (not a competing implementation), benchmark/wire-eligibility summary, architecture census and no-follow-up review.
  - Mark spec completed/ready false and archive according to Kiro workflow.
  - Completion means newly documented provider carriers are ordinary adapter/profile additions, not generic cache-affinity architecture work.
  - _Depends: 12.4_
