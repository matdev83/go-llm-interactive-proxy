# Implementation Plan

This plan targets the **post-`core-feature-ownership-full-closure` topology** and is written for a smaller instruction-following implementation model. Follow it literally. Do not recreate package ownership from the previous cache-affinity revision.

## Mandatory Execution Rules

1. **Hard predecessor gate:** no production work before `.kiro/specs/core-feature-ownership-full-closure/` is implemented/certified on `main` and its final zero-debt ownership census/core-admission ratchets exist.
2. If #429 rebranding already landed, use mechanically renamed `aip*` paths/symbols. Never recreate retired `lip*` names solely because this SDD was authored before that rename.
3. Do not create `internal/core/cacheaffinity`, a cache-specific executor stage/field, a cache-specific `SecurityRuntime` field, `execbackend.Backend.ResolveDownstreamCacheAffinity`, a cache-specific `ProcessServices` field, or a new cache-affinity extension plane.
4. Reuse existing `PlaneAttemptTransforms`; cache-affinity derivation/policy/telemetry lives in `internal/plugins/features/downstreamcacheaffinity` and standard construction lives in `internal/standardplugins/featurehost`.
5. The only permitted new core-facing capability is a **generic bounded immutable backend-feature ID list**, and only if Task 1 proves no equivalent post-closure carrier exists.
6. Provider wire names live only in provider profile/backend/connector code. The feature algorithm receives only generic backend features and stable backend prefixes.
7. Generic synthesis input is admitted `AuthoritativeSessionID` only. Never use client hint, A-leg, principal/user/IP/request ID, workspace metadata or SafeMetadata.
8. Generated value is exactly `aipca1_` + full SHA-256 raw-base64url digest = 50 characters. Never truncate.
9. HMAC domains are exactly `aiproxer/downstream-cache-affinity/key/v1\x00` and `aiproxer/downstream-cache-affinity/value/v1\x00`.
10. The feature never receives the secure-session fingerprint root. It receives only a 32-byte domain-derived subkey from a generic featurehost composition capability.
11. Direct OpenAI Responses PCK forwarding must be repaired before its synthesis path is certified.
12. The `provider-profile` production lifecycle must preserve compiled semantics before profile cache-affinity rows are accepted. Reuse an already-landed bulk-provider repair instead of duplicating it.
13. No new backend-plugin value protobuf field/protocol minor. Add only negotiated `downstream_cache_affinity_v1` at existing semantic-extension minor 6.
14. OpenRouter uses JSON body `session_id` only; explicit existing `openrouter.session_id` wins over effective PCK; no `x-session-id`.
15. TDD/characterization first for every brownfield seam. No unrelated cleanup/refactor.
16. If a STOP condition fires, stop that wave and repair the SDD; do not invent another framework or core exception.

## Target File Map

Use these post-full-closure targets or their direct post-#429 renames:

| Concern | Target |
|---|---|
| Generic backend feature IDs | `pkg/lipsdk/backendfeature/` (only if no equivalent exists) |
| Generic executor backend feature value | `internal/core/execbackend/backend.go` |
| Generic attempt metadata projection | `pkg/lipsdk/request/attempt_transform.go`, `internal/core/runtime/executor_attempt_transform.go` |
| Feature implementation | `internal/plugins/features/downstreamcacheaffinity/` |
| Standard composition | `internal/standardplugins/featurehost/cacheaffinity.go` + featurehost input file |
| Generic secure-root domain-key adapter | post-closure secure-session/runtimebundle composition owner |
| Direct OpenAI PCK | `internal/plugins/backends/openairesponses/invoke.go`, constructor |
| Provider-profile lifecycle | `internal/standardplugins/provider_profile_binding.go`, `provider_profiles.go`, `standard_contributions.go` |
| Provider schema/catalog | `internal/providerprofiles/schema.go`, `catalog.json` |
| Profile-compatible projection | `internal/standardplugins/provider_profile_binding.go`, `internal/plugins/backends/openaicompat/*` |
| OpenRouter carrier | `connectors/openrouter/internal/service/body.go`, `service.go` |
| Backend-plugin feature | `pkg/lipsdk/backendplugin/*`, `internal/infra/backendplugins/adapter/backend.go` |
| Feature telemetry | feature observer + `internal/standardplugins/featurehost`/existing infra metrics adapter |
| Reusable TCK | `internal/testkit/contract/cacheaffinity/` |

---

# 0. Hard Predecessor and Topology Gate

- [ ] 0.1 Verify full core-ownership closure is actually implemented
  - Read the completed/archived `.kiro/specs/core-feature-ownership-full-closure/` from the implementation branch, not PR text.
  - Record final predecessor SHA and ownership-census path in the implementation PR/evidence.
  - Assert the predecessor's core-admission manifest/ratchets are green and `internal/standardplugins/featurehost` is the standard feature-aware composition owner.
  - Assert `runtimebundle.ProcessServices` does not contain optional per-feature service fields except the single standard featurehost handle allowed by the predecessor.
  - If predecessor is not complete, STOP. Do not implement this SDD on the old topology.
  - _Boundary: architecture gate_
  - _Depends: implemented/certified core-feature-ownership-full-closure_
  - _Validation: predecessor final architecture tests; `make arch-report`; focused featurehost tests_

- [ ] 0.2 Re-inventory renamed/moved owners on current implementation branch
  - Map the file targets above to current names after any rebranding/closure moves.
  - Record current `PlaneAttemptTransforms` declaration, attempt metadata builder, standard featurehost generation composition, secure-session fingerprint-root composition owner, backend-plugin adapter, provider-profile lifecycle, direct OpenAI serializer and OpenRouter body code.
  - Record whether a generic backend feature/capability ID carrier already exists. If equivalent bounded immutable metadata exists, mark Tasks 2.1-2.3 as reuse/adaptation work and do not create another carrier.
  - _Boundary: brownfield inventory_
  - _Depends: 0.1_
  - _Validation: repository import/symbol scan; `go list` where relevant_

---

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

- [ ] 1.4 Characterize real `provider-profile` production binding
  - Start from config `kind: provider-profile`, `config.profile: <fixture>`.
  - Drive through `PrepareProviderProfiles` and the same standard registry/lifecycle/candidate construction used in production.
  - Pin a compiled-only disabled capability, a safe static header and representative quirk/dialect so lossy lowering is observable.
  - If bulk-provider work already fixed the path, make this GREEN proof authoritative and mark Tasks 6.1-6.4 production repair as verification-only.
  - If still lossy, keep RED and implement Task 6.
  - Source config immutability is mandatory.
  - _Requirements: 6.8,6.10,7.10_
  - _Boundary: provider profile production lifecycle_
  - _Depends: 0.2_

- [ ] 1.5 Capture baseline cost and ownership evidence
  - Record non-test core LOC/budget/manifest status from predecessor before this feature.
  - Benchmark an empty/no-op AttemptTransform baseline and current PCK serializers with `-benchmem`.
  - Record that no cache-affinity-specific production package/symbol exists in core.
  - Final Task 12 must prove core feature-specific LOC remains unchanged except for the generic backend-feature metadata seam if Task 2 is required.
  - _Requirements: 2,9,11_
  - _Boundary: performance/architecture evidence_
  - _Depends: 0.2_

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
  - Frozen ID `downstream-cache-affinity`; `TransformOrder = 1_000_000`; failure mode fail-closed for canonical PCK conflicts/internal impossible errors.
  - Algorithm exactly from design: existing PCK -> preserve; missing generic backend feature -> unsupported; invalid/no prefix -> no synthesis; nil deriver/no authoritative session -> disabled; otherwise derive from `BackendPrefixes[0]` + `Session.AuthoritativeSessionID` and set only `call.PromptCacheKey`.
  - Never read `ClientSessionHint`, A-leg, principal/scope/workspace/trace/model for derivation.
  - Unsupported/disabled/invalid backend support does not exclude candidate; PCK alias conflict returns error.
  - Observer event contains only bounded source/outcome/backend ID.
  - _Requirements: 1,2,3,4,5,9,10_
  - _Depends: 1.1,2.3,3.1_
  - _Validation: feature transform table + race-safe observer tests_

- [ ] 3.3 Build an ordinary `FeatureBundle`
  - Create `bundle.go` that contributes exactly one transform to existing `PlaneAttemptTransforms`.
  - No new plane, lifecycle, goroutine, state store, config decoder or service registry.
  - Prove disabled/no-deriver composition can omit the transform cleanly.
  - Add bundle plane-parity/typed-nil tests per repository conventions.
  - _Requirements: 2.1-2.4,11.2_
  - _Depends: 3.2_
  - _Validation: feature bundle tests + existing plane parity checks_

---

# 4. Compose the Feature Through Standard `featurehost`

- [ ] 4.1 Add/reuse a generic domain-key derivation capability
  - In post-closure featurehost process input add a narrow internal `DomainKeyDeriver func(domain string) ([32]byte,error)` (or reuse an existing equivalent).
  - In the existing secure-session/runtimebundle key owner, build the capability from already-resolved fingerprint root `fp` using one HMAC-SHA256 over the validated domain.
  - Domain bound <=128; reject empty/control/NUL. Nil capability when no root authority exists.
  - Generic runtimebundle code must contain no cache-affinity feature ID, PCK logic or provider names.
  - Never expose/copy raw `fp` into featurehost/feature.
  - _Requirements: 2,4.4-4.7,9_
  - _Depends: 0.1,1.5_
  - _Validation: generic domain-key deterministic/separation tests; security ownership tests_

- [ ] 4.2 Add standard featurehost cache-affinity adapter
  - Create `internal/standardplugins/featurehost/cacheaffinity.go` (or post-rebrand equivalent).
  - Ask the generic key capability exactly once for `aiproxer/downstream-cache-affinity/key/v1\x00` during process/standard-feature construction.
  - Construct `downstreamcacheaffinity.Deriver` from returned subkey.
  - Construct safe feature observer backed by existing infra metrics and merge the feature's ordinary bundle into standard generation composition.
  - Missing generic key capability => no synthesis transform; do not fail base proxy startup merely for this optimization.
  - No cache-specific field in `ProcessServices`/Executor/SecurityRuntime.
  - _Requirements: 2,4,10_
  - _Depends: 3.3,4.1_
  - _Validation: featurehost process/generation tests + overlapping generation test_

- [ ] 4.3 Prove standard integration and raw-session scrub
  - End-to-end standard distribution test: authoritative session + capable fake backend -> exact deterministic generated PCK reaches backend `Open`; every session field is empty there.
  - Existing explicit PCK -> unchanged.
  - Client hint without authoritative session -> no generated PCK.
  - Two backend prefixes -> namespace follows each selected backend; parallel copies do not share mutable state.
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

# 6. Repair or Verify Real `provider-profile` Lifecycle

- [ ] 6.1 Implement one real `ProviderProfileKind` lifecycle factory if still missing
  - In `provider_profile_binding.go`, add `LifecycleProviderProfile(instanceID,n,upstream,deps)`.
  - Exact flow: `profileReference` -> `ProviderProfileCatalog` -> exact lookup -> `CompileProviderProfile` -> `BuildProviderProfileBackend` -> `BackendBuildResult`.
  - Do not duplicate profile validation/capability/header/quirk/dialect policy.
  - _Requirements: 6.8,6.10_
  - _Depends: 1.4 RED unless already GREEN from bulk-provider implementation_

- [ ] 6.2 Register one lifecycle contribution
  - Register exactly one `ProviderProfileKind` standard backend contribution with normal inference/static-credential metadata.
  - Keep compatible-family contributions/profile IDs; no factory per profile/provider.
  - _Depends: 6.1_

- [ ] 6.3 Stop profile rows from being rewritten to generic compatible YAML
  - Change preparation/expansion into validation + clone preservation.
  - Validate profile reference and compile it, but do not change `row.Kind` and do not replace `row.Config`.
  - Arbitrary custom-compatible rows unchanged; source config immutable.
  - `ProfileConfigNode` remains family-builder/test helper only.
  - _Depends: 6.2_

- [ ] 6.4 Make the production-path regression GREEN
  - Run Task 1.4 fixture through actual registry/lifecycle/candidate build.
  - Prove row remains `provider-profile` and complete compiled disabled capability/header/quirk/dialect semantics survive.
  - If Tasks 6.1-6.3 were already implemented by bulk-provider work, this task still runs and certifies them; do not change production code unnecessarily.
  - _Validation: provider-profile standardplugins + runtimebundle candidate tests_
  - _Depends: 6.3 or pre-existing equivalent repair_

---

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
  - Pass the validated selected flavor projection through the existing profile-aware OpenAI-compatible builder.
  - Do not serialize it into generic compatible YAML.
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
  - Extend Task 6.4 fixture with a profile cache-affinity projection and run through config preparation + registry/lifecycle/candidate construction.
  - Prove generic backend feature is present and the real serializer projects PCK.
  - Direct `BuildProviderProfileBackend` unit alone is not acceptance evidence.
  - _Depends: 8.2_

---

# 9. Add/Augment Frozen Initial Profile Rows

- [ ] 9.1 Add/augment `fireworks`, `xai`, `xai-responses`, `mistral`, `runinfra`
  - Use exact family/base/env/projection matrix in design/research.
  - If a row already exists from bulk provider work, augment it; preserve stricter static inventory and disabled capabilities.
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
  - Add `FeatureDownstreamCacheAffinity = "downstream_cache_affinity_v1"` at existing semantic-extension minimum minor 6.
  - Host supports negotiation; no new invocation field/protobuf message and no protocol minor increment.
  - _Requirements: 8_
  - _Depends: 2.1_

- [ ] 10.2 Map negotiated plugin feature to generic backend feature metadata
  - In backend-plugin adapter, successful negotiated new feature **and at least one stable route/backend prefix** => append generic `backendfeature.DownstreamCacheAffinityV1`.
  - No feature/no prefix/old peer => absent.
  - Do not add a cache-specific `execbackend.Backend` resolver.
  - _Depends: 10.1,2.2_

- [ ] 10.3 Implement OpenRouter body precedence and advertise feature
  - `Describe`: existing required features + new cache-affinity feature.
  - Body: explicit existing `openrouter.session_id` > `call.PromptCacheKeyValue()` > omit.
  - Max256; JSON `session_id` only; no `x-session-id`.
  - Add explicit/generated/absent/conflict/oversize tests.
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
  - Cover deterministic derivation, explicit precedence, capability gating, no-session, namespace separation, serializer bounds, connector negotiation, session scrub and unknown/custom negatives.
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
  - Reject: `internal/core/cacheaffinity`; cache-specific Executor/SecurityRuntime/ProcessServices/execbackend fields/callbacks; cache-specific new plane; direct runtimebundle concrete feature construction; feature imports of core/runtimebundle; providerprofiles import of feature; provider wire literals in generic core/feature policy; new plugin value DTO/minor; raw session backend wire; generated PCK residency/session/continuation use; lossy provider-profile rewriting.
  - Preserve predecessor core-admission manifest and budget. Generic backend feature metadata is explicitly classified as a generic extension mechanism, not cache ownership.
  - _Requirements: 2,11_
  - _Depends: all production tasks_

- [ ] 12.2 Benchmark hot paths
  - Bench feature transform: explicit PCK, generated, no backend feature, no authoritative session.
  - Bench direct OpenAI/profile serializers that consume PCK.
  - Assert no DB/network/filesystem/goroutine/timer and bounded allocations; subkey derivation absent from request benchmark.
  - Compare against Task 1.5 baseline on same host/toolchain where practical.
  - _Requirements: 9_
  - _Depends: 11_

- [ ] 12.3 Run full certification
  - Focused feature/profile/OpenAI/OpenRouter/backendplugin tests.
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
  - Record exact merge SHA, provider-profile ownership outcome (this SDD vs bulk-provider), benchmark summary, architecture census and no-follow-up review.
  - Mark spec completed/ready false and archive according to Kiro workflow.
  - Completion means newly documented provider carriers are ordinary adapter/profile additions, not generic cache-affinity architecture work.
  - _Depends: 12.4_
