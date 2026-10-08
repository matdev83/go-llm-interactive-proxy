# Design Document

## Overview

This design completes AIProxer's downstream prompt-cache locality optimization **on top of the lean-core architecture defined by `pre-oss-core-slimming` and `core-feature-ownership-full-closure`**.

The original pre-lean-core draft scheduled too much optional-feature knowledge in generic core; the subsequent lean-core revision fixed ownership but predates the **completed marker-aware provider-profile lifecycle**, the **large-payload wire eligibility gate**, and other merged code: a new `internal/core/cacheaffinity` package, a cache-specific `SecurityRuntime` field, a cache-specific `execbackend.Backend` resolver, a cache-specific executor helper/stage, and a core metrics callback. Those choices contradict the final Core Admission Test.

The corrected architecture is:

```text
secure-session fingerprint root
        |
        | generic domain-key derivation capability
        v
standardplugins/featurehost
        |
        +--> downstreamcacheaffinity standard feature
                - keyed derivation
                - candidate AttemptTransform
                - feature-owned telemetry
                |
                v
        existing PlaneAttemptTransforms
                |
                v
core generic extension runner
        |
        +--> request.AttemptMeta
              - authoritative SessionView
              - backend id/prefixes/model
              - generic bounded BackendFeatures
        |
        v
Call.PromptCacheKey (fill-only)
        |
        v
normal request hooks / admission / adaptation / session scrub
        |
        v
backend/profile/connector serializer
        |
        v
provider-specific documented carrier
```

No cache-affinity algorithm or policy remains in core.

> **CURRENT BASELINE / IMPLEMENTATION GATE (2026-10-08, main `79a60863`):** Core ownership closure is **already completed** (#598/#599; blocker remediation #600) and archived at `.kiro/specs/archive/core-feature-ownership-full-closure/`. Reconfirm the live ownership census, core-admission ratchets and standard `featurehost` APIs on the implementation branch; do **not** wait for or reimplement closure. Bulk-provider lifecycle (#619/#622) is also complete and must be reused. If pending rebranding #641 lands, use actual renamed code paths while keeping the unimplemented `aipca1_`/HMAC contract intact.

## Goals

- Preserve explicit PCK/provider affinity before generated fallback.
- Generate one stable provider-scoped opaque value from admitted session authority without exposing raw session IDs.
- Use the existing candidate-aware attempt-transform plane rather than a new core stage/plane.
- Keep cache-specific composition in the standard feature host.
- Add only a generic immutable backend-feature metadata seam if the final post-closure tree still lacks one.
- Repair direct OpenAI Responses PCK forwarding.
- Reuse/certify the completed marker-aware `provider-profile` production lifecycle without adding a parallel factory.
- Add typed provider-profile cache-affinity projection and the frozen initial provider matrix.
- Preserve executable connector ABI value compatibility and use feature negotiation only for capability opt-in.
- Preserve prompt-cache residency/control/keep-warm as separate authorities.
- Avoid introducing new legacy branding in a not-yet-implemented wire contract.

## Non-Goals

- No new cache-residency identity or keep-warm semantics.
- No cache-hit guarantee.
- No cache-affinity store/database.
- No raw session forwarding to backends/connectors.
- No generic inbound provider-header capture framework.
- No provider-name switch in core or the feature algorithm.
- No second provider-profile catalog or transform DSL.
- No new canonical `Call` field beyond existing `PromptCacheKey`.
- No new backend-plugin value DTO or protocol-minor bump.
- No new cache-specific `execbackend.Backend` callback.
- No new cache-specific `runtime.Executor`, `SecurityRuntime`, `ProcessServices`, or `lipruntime.Options` field.
- No cache-specific extension plane.

---

# 1. Post-Full-Closure Boundary Map

| Concern | Owner | Cache-affinity change? |
|---|---|---:|
| Session authority / fingerprint-root lifecycle | existing secure-session kernel + infra | generic key derivation exposure only |
| Generic extension execution | core | reused |
| Candidate-aware feature execution | existing `PlaneAttemptTransforms` | reused |
| Backend semantic feature facts | generic SDK/execbackend metadata | small generic addition only if absent |
| Derivation + synthesis policy | `internal/plugins/features/downstreamcacheaffinity` | new |
| Standard feature construction | `internal/standardplugins/featurehost` | new feature adapter |
| Protocol-neutral value | `lipapi.Call.PromptCacheKey` / semantic carrier | reused |
| Provider wire carrier | backend/profile/connector | extended |
| Provider-profile catalog/compiler | `internal/providerprofiles` | typed data extension |
| Provider-profile production binding | `internal/standardplugins` | reuse existing marker-aware bridge; extend compiled-profile projection |
| Cache residency/control | `pkg/lipsdk/promptcache` + backends | unchanged |
| Keep-warm | post-full-closure standard feature | unchanged |
| Feature telemetry | feature observer + standard/infra metrics adapter | new outside core |

### Forbidden final shape

The implementation must not create any of these production symbols/paths:

```text
internal/core/cacheaffinity/
runtime.SecurityRuntime.DownstreamCacheAffinity*
runtime.Executor.*CacheAffinity*
execbackend.Backend.ResolveDownstreamCacheAffinity
internal/core/runtime/executor_cache_affinity.go
internal/core/runtime.MetricsSink.OnDownstreamCacheAffinity
runtimebundle.ProcessServices.*CacheAffinity*
```

Architecture tests must ratchet these absences.

---

# 2. Reuse `PlaneAttemptTransforms`

Current SDK already provides a candidate-aware transform:

```go
type AttemptMeta struct {
    TraceID         string
    ALegID          string
    CandidateKey    string
    BackendID       string
    BackendPrefixes []string
    Model           string
    ReplaySupport   lipapi.ReasoningReplaySupport
    Scope           scope.PrincipalScopeView
    Session         session.SessionView
    Workspace       workspace.WorkspaceView
}

type AttemptTransform interface {
    ID() string
    Order() int
    FailureMode() hooks.FailureMode
    HandleAttempt(context.Context, *lipapi.Call, AttemptMeta, Services) (AttemptDecision, error)
}
```

Core already obtains transforms from `PlaneAttemptTransforms`, builds authoritative candidate metadata, and runs them before candidate admission/upstream open. That is the correct generic extension seam.

### Required post-closure extension

If the final full-closure tree does not already expose generic backend semantic features in `AttemptMeta`, add only:

```go
BackendFeatures []backendfeature.ID
```

`candidateAttemptMeta` defensively clones the immutable backend feature list. No cache-affinity logic is added there.

### Timing and precedence constraint

The cache-affinity transform runs late among standard attempt transforms and is **fill-only**. Freeze:

```go
const TransformOrder = 1_000_000
```

Before production changes, characterize all production code after the attempt-transform stage and prove no current request-part hook or later generic stage creates/replaces `PromptCacheKey`. If that premise is false on the implementation branch, STOP and revise this SDD. Do not invent a second cache-specific late executor stage.

Tests must prove a generated PCK survives:

```text
AttemptTransform
 -> candidate admission
 -> request-part hooks
 -> rederived admission
 -> authority/clamps
 -> AdaptCallForCandidate
 -> final raw-session scrub
 -> backend Open/serializer
```

The feature never writes provider wire fields directly.

### Generation enablement and large-payload wire fast path (new mandatory integration constraint)

`pkg/lipsdk/feature/plane_manifest.go` classifies `PlaneAttemptTransforms` as `RequestBodyCanonicalRequired`; `internal/core/largebody/authority_gate.go`/`eligibility.go` decline the direct wire fast path when the plane has an occupant. This is the correct behavior: direct wire execution cannot synthesize a canonical `PromptCacheKey` by pretending the transform ran.

Therefore **do not register the transform unconditionally**. Standard cache-affinity synthesis is opt-in through an **enabled feature registration** (or an equivalent existing typed, generation-pinned opt-in), default absent/disabled. The configuration/registry layer may supply a validated empty/static feature contribution, but exactly **one runtime-bound transform** is added by `featurehost` only for the enabled generation, and never duplicated between the registry and host. An absent/disabled registration contributes zero attempt-transform occupants. `featurehost.GenerationInput.Registrations` is the existing source of generation enablement. Avoid a new dedicated core config field or feature plane.

While enabled, the canonical-required attempt-transform plane may cause the full request to use the canonical path, including for routed unsupported candidates; accept and benchmark this explicit opt-in tradeoff rather than weakening wire eligibility, adding a provider-specific bypass, or silently losing generated affinity. A subsequent optimization may only be considered after separate correctness proof, not within this SDD.

Test enabled/disabled generation reload and `server.large_payload_fast_path.enabled`: disabled preserves wire eligibility, enabled declines wire acceleration and invokes the canonical transform, and explicit PCK serializer forwarding works without synthesis enabled.

---

# 3. Generic Immutable Backend Feature Metadata

## 3.1 Package

Default target if no equivalent exists post-closure:

```text
pkg/lipsdk/backendfeature/feature.go
pkg/lipsdk/backendfeature/feature_test.go
```

Contract:

```go
type ID string

const (
    MaxIDBytes = 128
    DownstreamCacheAffinityV1 ID = "downstream_cache_affinity_v1"
)

func (id ID) Validate() error
func Normalize(in []ID) ([]ID, error)
func Contains(in []ID, want ID) bool
```

Rules:

- ASCII token-like bounded ID; no controls/NUL/whitespace-only;
- normalize = validate + dedupe + stable sort + defensive copy;
- zero/nil means no features;
- no callbacks, configuration, services, request-time lookup, provider names, or arbitrary values.

This is not a second feature framework. It is the in-process representation of the feature-negotiation concept already present in the executable-backend ABI.

## 3.2 Executor-facing backend value

Add only a generic value field if absent:

```go
type execbackend.Backend struct {
    ...
    Features []backendfeature.ID
}
```

Add `CloneBackendFeatures(be Backend) []backendfeature.ID` or equivalent. Construction paths normalize once. Runtime only reads the immutable list.

Do **not** add `ResolveFeature`, a map of values, callbacks, per-request negotiation, or a cache-specific resolver.

## 3.3 Attempt metadata

`candidateAttemptMeta` copies `Backend.Features` into `request.AttemptMeta.BackendFeatures`. The cache feature checks only `backendfeature.Contains(meta.BackendFeatures, backendfeature.DownstreamCacheAffinityV1)`.

This generic metadata is the only core-facing capability change owned by this SDD.

---

# 4. Feature-Owned Package

Create under the post-full-closure feature tree:

```text
internal/plugins/features/downstreamcacheaffinity/
├── derive.go
├── transform.go
├── observer.go
├── bundle.go
├── derive_test.go
├── transform_test.go
└── bundle_test.go
```

The feature package may import only standard library, `pkg/lipapi`, `pkg/lipsdk/*`, and feature-local packages. It must not import `internal/core`, `runtimebundle`, provider profiles, provider backends, or provider wire packages.

## 4.1 Frozen derivation

```go
const (
    GeneratedPrefix   = "aipca1_"
    GeneratedLength   = 50
    MaxNamespaceBytes = 128
)

type Deriver struct {
    subkey [32]byte
}

func NewDeriver(subkey [32]byte) *Deriver
func (d *Deriver) Derive(namespace, authoritativeSessionID string) (string, error)
```

`Deriver` receives an already-domain-derived subkey, never the secure-session root.

Value formula:

```text
digest = HMAC-SHA256(subkey,
    "aiproxer/downstream-cache-affinity/value/v1\x00" ||
    namespace || "\x00" || authoritative_session_id)

wire = "aipca1_" + base64.RawURLEncoding(full_32_byte_digest)
```

Full digest, no truncation. The prefix is 7 characters; SHA-256 rawurl without padding is 43; total is 50.

Namespace/session validation is bounded and allocation-light. No provider wire names appear in this package.

## 4.2 Decision enums and observer

Feature-owned types:

```go
type Source string
const (
    SourceNone                Source = "none"
    SourceExplicitPromptCache Source = "explicit_prompt_cache"
    SourceProxyGenerated      Source = "proxy_generated"
)

type Outcome string
const (
    OutcomeAppliedOrAvailable Outcome = "applied_or_available"
    OutcomeUnsupported        Outcome = "unsupported"
    OutcomeDisabled           Outcome = "disabled"
    OutcomeInvalid            Outcome = "invalid"
)

type Event struct {
    Source    Source
    Outcome   Outcome
    BackendID string
}

type Observer interface { OnDecision(Event) }
```

`SafeObserver`/nil no-op must panic-isolate observer code if repository conventions require it. Event never carries PCK/session/key/model/body.

## 4.3 Attempt transform

Conceptual implementation:

```go
type Transform struct {
    deriver  *Deriver
    observer Observer
}

func (t *Transform) ID() string { return "downstream-cache-affinity" }
func (t *Transform) Order() int { return TransformOrder }
func (t *Transform) FailureMode() hooks.FailureMode { return hooks.FailClosed }
```

Algorithm exactly:

1. nil call -> error;
2. `existing, err := call.PromptCacheKeyValue()`;
3. alias conflict -> observe `invalid`, return error;
4. existing non-empty -> observe `explicit_prompt_cache/applied_or_available`, return continue unchanged;
5. feature ID absent from `meta.BackendFeatures` -> observe `none/unsupported`, return continue;
6. `meta.BackendPrefixes` must contain at least one stable prefix; use trimmed `BackendPrefixes[0]` as namespace; invalid/empty/over-128/control -> observe `none/invalid`, return continue without synthesis;
7. nil deriver -> observe `none/disabled`, return continue;
8. trim `meta.Session.AuthoritativeSessionID`; empty -> observe `none/disabled`, return continue;
9. derive exact 50-char value;
10. set **only** `call.PromptCacheKey` to the generated value;
11. observe `proxy_generated/applied_or_available`;
12. return `AttemptContinue`.

Never use `ClientSessionHint`, `ALegID`, principal/scope, workspace, trace ID or model in derivation.

The transform must not exclude a candidate merely because synthesis is unsupported/disabled. Only canonical PCK alias conflict or impossible internal derivation invariant is an error.

## 4.4 Bundle

`FeatureBundle(deriver, observer)` contributes exactly one `request.AttemptTransform` to existing `PlaneAttemptTransforms`. No new plane, service, hook bus, process resource, goroutine or lifecycle.

---

# 5. Generic Domain-Key Derivation at Standard Feature Host

The feature must reuse the already-resolved secure-session fingerprint root without exposing that root outside its current security owner.

## 5.1 Narrow generic capability

At the post-full-closure featurehost input boundary add, if no equivalent exists:

```go
type DomainKeyDeriver func(domain string) ([32]byte, error)
```

or an equivalent small interface. It is an internal composition capability, not a public SDK registration and not request-visible.

Rules:

- input is a **NUL-free domain label**, non-empty, bounded (<=128 bytes), no controls/NUL; the secure-session owner appends exactly one terminal `\x00` to the validated label **inside HMAC input**;
- output is one 32-byte HMAC-SHA256 subkey;
- no method exposes the root or lets the caller compare/export it;
- no persistence/network/DB;
- nil means no keyed optional standard feature can be composed from this authority.

## 5.2 Runtimebundle implementation

**Verified path:** `internal/infra/runtimebundle/build_persistence.go` builds `secureSessionRuntime` through `buildSecureSessionRuntime` in `secure_session.go`. There, `fp := []byte(key)` is resolved from configured `secure_session.token_fingerprint_key` (or a process-local ephemeral memory-store key). `internal/infra/runtimebundle/process_services.go` builds persistence **before** `featurehost.NewProcess`, currently passing `featurehost.ProcessInput` without a domain-key port. Add the narrow generic closure at this security owner, pass it through the existing process construction call to a new generic featurehost capability, and keep the original root out of `featurehost` and the feature.

The secure-session composition owner creates a generic closure conceptually:

```go
func(domainLabel string) ([32]byte, error) {
    // validate a bounded NUL-free label; append terminal byte only after validation
    return HMAC_SHA256(fp, domainLabel + "\x00"), nil
}
```

No cache-affinity literal is present in generic runtimebundle code. The root is captured by the closure and is not returned.

## 5.3 Featurehost adapter

Create/extend:

```text
internal/standardplugins/featurehost/cacheaffinity.go
```

The standard feature host asks:

```text
DomainKeyDeriver("aiproxer/downstream-cache-affinity/key/v1")
```

and constructs the feature `Deriver` from the returned subkey. Its HMAC input remains **exactly** `aiproxer/downstream-cache-affinity/key/v1\x00`, because the secure-session owner appended the NUL; never pass a NUL-bearing label into the validator. It wires the feature-owned observer and merges the feature's ordinary `FeatureBundle` into standard generation composition.

If the generic key deriver is absent, do not construct a synthesis-capable transform; explicit PCK forwarding in backends still works independently.

No raw fingerprint root enters featurehost or the feature package. The closure's lifetime follows the owning process and must be disposed with the existing process close; do not introduce a new cache-affinity store/secret or an `execbackend.Backend`/`ProcessServices` cache field.

---

# 6. Direct OpenAI Responses

## 6.1 Repair explicit PCK forwarding

Current `internal/plugins/backends/openairesponses/invoke.go` does not serialize the existing PCK semantic. At `ParamsForCall`:

1. resolve `PromptCacheKeyValue()` once;
2. propagate alias conflict with backend context;
3. empty -> omit;
4. >64 -> pre-output validation error;
5. <=64 -> assign typed SDK `ResponseNewParams.PromptCacheKey`.

Do not use untyped JSON override when the SDK exposes a typed field.

## 6.2 Advertise synthesis capability

The direct OpenAI Responses backend construction appends:

```go
backendfeature.DownstreamCacheAffinityV1
```

to its generic immutable backend features. Its existing stable backend prefix is the derivation namespace. No cache-specific resolver is added.

Direct OpenAI Chat is not enabled by this task unless separately covered by frozen provider evidence.

---

# 7. Reuse the Completed Marker-Aware `provider-profile` Lifecycle

The September 1 SDD's requested dedicated `LifecycleProviderProfile` and no-kind-rewrite design conflicts with completed production code. #619/#622 already implemented a **different, semantics-preserving single-owner lifecycle** and real candidate regression coverage. Do not replace it.

## 7.1 Actual production path (now normative)

```text
operator config kind: provider-profile, config.profile: <id>
  -> PrepareProviderProfilesWithCatalog / ExpandProviderProfileRowsWithCatalog
  -> compile catalog profile and create compatible-family config node
  -> add reserved YAML anchor lip_profile_<id> and comment lip:provider-profile:<id>
  -> rewrite PREPARED CLONE to matching compatible-family kind
  -> registered wrapCompatibleLifecycle(family, base)
  -> resolveProviderProfile(marker) -> CompileProfile
  -> buildProviderProfileBackendWithNode(compiled, instanceID, prepared node, upstream, deps)
  -> return existing BackendBuildResult
```

Source `config.Config`/user row MUST remain untouched. Marked prepared rows legitimately have compatible-family kinds; identity is carried by the reserved marker and reconstructed by the established lifecycle wrapper, not by retaining the source `provider-profile` kind. Unmarked arbitrary compatible rows still use the original family lifecycle. The existing wrappers are registered for the four compatible families in `internal/standardplugins/standard_contributions.go`; do not register a second profile kind/factory.

## 7.2 Cache-affinity extension of existing compiled-profile owner

Add `Profile.CacheAffinity` to declarative provider-profile data. Preserve it as a validated value on `CompiledProfile` and consume it **only in `buildProviderProfileBackendWithNode` / the existing profile-aware family builders**, alongside compiled capabilities, headers, quirks and dialects. Never trust the generic prepared YAML node as the sole source of this new typed policy. For profile cache projection the existing family `wrapCompatibleLifecycle` must continue resolving the compiled profile before backend construction.

Reuse current prepared-row markers and source-config immutability. Extend `internal/standardplugins/provider_profile_binding_test.go` and `internal/infra/runtimebundle/provider_profile_candidate_test.go` to exercise actual configured `kind: provider-profile` + expansion + wrapper + backend construction + PCK projection, not only `BuildProviderProfileBackend` directly.

## 7.3 Adversarial compatibility tests

Prove catalog handle/marker round trips, preserved disabled capability ceilings and static headers, correct dialect/quirk projection, missing catalog/unknown profile rejection, rejected forged marker on unrelated custom-compatible rows, wrong-family marker refusal, unchanged unmarked custom-compatible behavior, and source-config immutability. Do not create profile constructors per provider, a duplicate profile registry or a new routing framework.

The bulk-provider workstream is **historical completed evidence**, not an outstanding task or a dependency on another unimplemented lifecycle.

# 8. Typed Provider `cache_affinity` Schema

In `internal/providerprofiles/schema.go` add:

```go
type CacheAffinityTransport string
const (
    CacheAffinityTransportJSONField  CacheAffinityTransport = "json_field"
    CacheAffinityTransportHTTPHeader CacheAffinityTransport = "http_header"
)

type CacheAffinityProjection struct {
    Enabled             bool                   `json:"enabled,omitempty" yaml:"enabled,omitempty"`
    Transport           CacheAffinityTransport `json:"transport,omitempty" yaml:"transport,omitempty"`
    WireName            string                 `json:"wire_name,omitempty" yaml:"wire_name,omitempty"`
    MaxLength           int                    `json:"max_length,omitempty" yaml:"max_length,omitempty"`
    AllowProxySynthesis bool                   `json:"allow_proxy_synthesis,omitempty" yaml:"allow_proxy_synthesis,omitempty"`
}

type CacheAffinity struct {
    Chat      CacheAffinityProjection `json:"chat,omitempty" yaml:"chat,omitempty"`
    Responses CacheAffinityProjection `json:"responses,omitempty" yaml:"responses,omitempty"`
}
```

Add `Profile.CacheAffinity CacheAffinity`. No schema-version bump.

### Layering

`internal/providerprofiles` must not import the feature package. Define locally:

```go
const MinCacheAffinityValueLength = 50
```

and cross-ratchet it in architecture tests against `downstreamcacheaffinity.GeneratedLength`.

Validation:

1. disabled = strict zero subordinate fields;
2. enabled transport is exactly JSON field or HTTP header;
3. wire name required;
4. HTTP header uses existing safe-header validation;
5. JSON field matches `^[A-Za-z_][A-Za-z0-9_]{0,127}$`;
6. `MaxLength >= 50 && MaxLength <= MaxStringBytes`;
7. OpenAI Chat family enables only Chat;
8. OpenAI Responses family enables only Responses;
9. other v1 families reject enabled projection;
10. synthesis requires enabled projection.

---

# 9. Profile-to-Compatible Projection

Use the existing `wrapCompatibleLifecycle` -> `buildProviderProfileBackendWithNode` -> `profileFamilyBuilders` path; do not create dedicated xAI/Mistral/Fireworks/RunInfra backend packages. Ensure typed cache projection is passed from immutable compiled profile (not inferred from a generic compatible YAML node) and that the resulting serializer is only active on marked profile-built instances.

For the selected projection:

- `Enabled && AllowProxySynthesis` -> append `backendfeature.DownstreamCacheAffinityV1` to constructed backend features;
- backend prefix/profile ID remains the stable synthesis namespace;
- serializer resolves `call.PromptCacheKeyValue()` once;
- empty -> no wire option;
- length > `MaxLength` -> pre-output error;
- JSON -> `option.WithJSONSet(WireName, value)`;
- header -> `option.WithHeader(WireName, value)`.

Arbitrary `custom-*-compatible` rows receive neither the profile projection nor the backend feature flag.

The profile compiler remains the semantic authority. Do not serialize `cache_affinity` into generic compatible YAML.

---

# 10. Frozen Initial Profile Matrix

Add/augment in `internal/providerprofiles/catalog.json`:

```text
fireworks
  family: openai-responses-compatible
  base: https://api.fireworks.ai/inference/v1
  env: FIREWORKS_API_KEY
  responses: json_field prompt_cache_key max=256 synthesis=true

xai
  family: openai-chat-compatible
  base: https://api.x.ai/v1
  env: XAI_API_KEY
  chat: http_header x-grok-conv-id max=256 synthesis=true

xai-responses
  family: openai-responses-compatible
  base: https://api.x.ai/v1
  env: XAI_API_KEY
  responses: json_field prompt_cache_key max=64 synthesis=true

mistral
  family: openai-chat-compatible
  base: https://api.mistral.ai/v1
  env: MISTRAL_API_KEY
  chat: json_field prompt_cache_key max=256 synthesis=true

runinfra
  family: openai-chat-compatible
  base: https://api.runinfra.ai/v1
  env: RUNINFRA_API_KEY
  chat: json_field prompt_cache_key max=64 synthesis=true
```

At the October 8 baseline, the 141-entry embedded catalog **already has `fireworks`, `xai`, and `mistral`**, but **not `xai-responses` or `runinfra`**. Augment exactly the three existing profiles, add only the two absent rows, and preserve existing disabled capabilities, model inventory, credentials, and endpoints. Recheck official provider contracts during implementation; cache-affinity must not broaden model capabilities.

Negative controls:

```text
direct Anthropic            -> no synthesis
direct Gemini               -> no synthesis
arbitrary custom-compatible -> no synthesis
profile without projection  -> no synthesis
```

---

# 11. OpenRouter + Executable Backend Negotiation

## 11.1 Connector body carrier

OpenRouter precedence:

1. existing explicit `openrouter.session_id` extension;
2. otherwise `call.PromptCacheKeyValue()`;
3. otherwise omit.

`connectors/openrouter/internal/service/body.go:applyOpenRouterBody` currently writes explicit `session_id` in a passthrough loop **before** a later generic extra-body extension loop, which can also write `session_id`. The implementation must make collision handling authoritative: reject conflicting aliases or deterministically preserve the explicit supported extension, including after the extra-body loop. No silent last-write-wins override. Cover empty, non-string and overlength values before provider I/O.

Project only JSON body `session_id`, max 256. Do not emit a duplicate `x-session-id`.

## 11.2 Feature flag, no value DTO

Add/reuse backend-plugin feature:

```go
FeatureDownstreamCacheAffinity = "downstream_cache_affinity_v1"
```

Minimum minor = existing semantic-extension minor **6**; the host already negotiates current minor **9**. Update centralized `featureMinorRequirements` in `pkg/lipsdk/backendplugin/protocol.go` and both the typed `ProtocolOffer.Features` and actual wire `NegotiateRequest.HostFeatures` in `pkg/lipsdk/backendplugin/host/session.go`, including tests for their parity and old peers. No protocol bump and no new protobuf value field.

OpenRouter `Describe` advertises it only after body behavior is implemented.

## 11.3 Host adapter mapping

`internal/infra/backendplugins/adapter` already receives immutable negotiation. If the feature is successfully negotiated, the existing PCK carrier is actually usable under negotiated protocol/semantic-extension compatibility, and a stable route/backend prefix exists, append `backendfeature.DownstreamCacheAffinityV1` to the constructed `execbackend.Backend.Features`.

No negotiated feature or no stable prefix -> no generic backend feature -> feature transform does not synthesize.

The plugin still receives only the existing PCK semantic; raw authoritative session is scrubbed before `Open`.

Old peers remain unchanged.

---

# 12. Observability Outside Core

The feature transform emits only feature-owned `Event` values. `internal/standardplugins/featurehost/cacheaffinity.go` supplies a safe observer backed by the existing infrastructure metrics implementation.

Recommended metric surface:

```text
downstream_cache_affinity_decision_total{source,outcome,backend}
```

Use the repository's existing bounded configured-backend label policy. Do not add raw model/PCK/session/request/residency values.

No method is added to `internal/core/runtime/metrics_sink.go`. Generic extension-runner telemetry remains unchanged.

---

# 13. Security and Authority Invariants

1. Raw `AuthoritativeSessionID` is visible to the feature transform only through the existing authoritative `AttemptMeta.Session` view and is never copied to provider wire.
2. Feature receives only a derived subkey, never the secure-session fingerprint root.
3. Generated value is provider/backend-prefix scoped.
4. `aipca1_...` is not authentication, authorization, a resume token, a B-leg ID, a cache residency target/generation/handle, or continuation state.
5. No generated value enters SafeMetadata, logs, metrics, persistence keys, filenames or provider model identities.
6. Provider-specific explicit carrier precedence remains adapter-owned (notably OpenRouter).
7. Existing final session scrub remains unchanged and tests prove it still removes all raw session fields.

---

# 14. Testing / TCK / Architecture Gates

## Feature matrix

| Existing PCK | Backend feature | Auth session | Expected |
|---|---:|---:|---|
| explicit | any | any | preserve |
| empty | present | present | generated exact 50-char PCK |
| empty | present | absent | none |
| empty | absent | present | none |
| alias conflict | any | any | error |

Prove namespace/session/key separation and exact deterministic vector.

## Timing characterization

Before implementation, prove:

- all current standard AttemptTransform orders are below `TransformOrder`;
- no production request hook/later generic stage writes `PromptCacheKey` after attempt transforms;
- `AdaptCallForCandidate` preserves an eligible PCK;
- final session scrub removes session fields without removing PCK;
- parallel arms receive independent call copies/metadata.

An architecture allowlist should fail if a new post-transform PCK writer is introduced without reviewing precedence.

## Provider-profile production path

Start from real config `kind: provider-profile`; pass through clone preparation and its reserved marker, existing compatible-family wrapper, registry/lifecycle/candidate construction; prove compiled capabilities/header/quirks/dialects and the new cache-affinity projection survive. Reject marker forgery, wrong-family and missing-catalog cases. **Do not assert the prepared row retains the source kind:** it intentionally does not. Direct builder tests alone are insufficient.

## Direct OpenAI

Explicit PCK, semantic alias, conflict, empty, len64, len65, generated integration, no direct-Chat accidental enablement.

## Profile projection

xAI header; xAI Responses JSON; Mistral/Fireworks/RunInfra JSON; unknown/custom negatives; strict validation; generated-length constant equality.

## OpenRouter / executable ABI

Explicit session wins; PCK fallback; body-only; <=256; extra-body `session_id` collision cannot bypass precedence; both host negotiation feature lists match; negotiated feature maps to generic backend feature only when PCK carrier usable; legacy peer does not; no new invocation DTO/protocol minor.

## Residency separation

Generated PCK never becomes TargetID/GenerationID/handle/timing and does not arm keep-warm without backend observation.

## Reusable TCK

Create/retain `internal/testkit/contract/cacheaffinity/` for provider-neutral feature/derivation/precedence/projection/connector/residency checks. Explicit enabled/disabled reload and large-payload wire eligibility/canonical fallback tests are mandatory. Offline only; no frontend × provider Cartesian suite.

## Architecture ratchets

Fail if:

- `internal/core/cacheaffinity` exists;
- core/runtimebundle contains cache-affinity algorithms/provider wire names;
- cache-specific fields appear on Executor/SecurityRuntime/ProcessServices/execbackend.Backend;
- a new cache-affinity plane exists;
- feature package imports `internal/core` or runtimebundle;
- providerprofiles imports feature/core cache-affinity implementation;
- `MinCacheAffinityValueLength != GeneratedLength`;
- raw session authority reaches backend wire;
- backend proto gains a cache-affinity value field/minor;
- generated PCK becomes residency/session/continuation authority;
- the existing marker-aware prepared-row rewrite loses compiled-profile semantics, permits marker forgery, or mutates source config;
- standard runtimebundle directly constructs/imports the concrete cache-affinity feature instead of featurehost.

---

# 15. Performance Contract

Generated attempt hot path:

```text
PromptCacheKeyValue()                 <= once in feature transform
backendfeature.Contains()             bounded small immutable slice
namespace/session validation          bounded
HMAC-SHA256(value)                    once
base64url encode 32 bytes             once
ordinary Call string assignment       once
serializer PromptCacheKeyValue()      once at selected backend boundary
```

Forbidden:

```text
DB / network / filesystem
request-time secure-session lookup
goroutine / timer / channel
prompt/tool hashing
unbounded map/cache/history
raw-root HMAC derivation per request
feature service lookup per request
```

Subkey is derived once during standard feature composition. Benchmark explicit/generated/unsupported/no-session paths and participating serializers with `-benchmem`. No pooling unless benchmarks prove material value.

---

# 16. Implementation Ordering

1. Verify full-closure predecessor and regenerate lean-core ownership baseline.
2. Characterize attempt-transform timing/PCK writers/adaptation/session scrub and backend feature-negotiation facts.
3. Add generic immutable backend-feature metadata only if no equivalent current-main seam exists.
4. Implement feature-owned derivation/observer/attempt transform/bundle with **explicit generation opt-in** and no default plane occupancy.
5. Add generic domain-key derivation capability and featurehost composition.
6. Repair direct OpenAI explicit PCK forwarding and mark generic backend feature.
7. Verify and reuse the existing marker-aware provider-profile production binding; add projection to its compiled profile-aware builders.
8. Add typed profile schema/projection and backend feature marking.
9. Augment `fireworks`/`xai`/`mistral` and add `xai-responses`/`runinfra` on the existing catalog.
10. Add OpenRouter body behavior + negotiated feature mapping.
11. Add feature-owned metrics/TCK/residency/continuation/parallel regressions.
12. Run perf/architecture/full QA, regenerate ownership census, independent review, archive.

Profile schema/data work must use and certify the **already-shipped** marker-aware production profile lifecycle; do not schedule a new lifecycle. Full-closure predecessor is already certified/archived. The initial RED gates now include request pipeline PCK writer ownership and wire-fast-path activation semantics. Implementation remains in the later split milestone.

---

# 17. Design Validation Verdict

### Core ownership

**PASS.** Optional derivation/policy/telemetry lives in the standard feature; core only executes the existing attempt-transform plane and, if necessary, carries one generic immutable backend-feature list justified by existing executable feature negotiation.

### Security

**PASS.** The feature receives admitted SessionView plus a domain-derived subkey, never the raw root; provider receives only the HMAC pseudonym; final session scrub remains authoritative.

### Compatibility

**PASS.** No feature flag means no synthesis. Explicit PCK wins. Old executable peers remain unchanged. Provider wire projection is opt-in and adapter-owned.

### Rebranding

**PASS.** The unimplemented wire contract starts with `aipca1_` and `aiproxer/...` domains; pending #641 may rename package paths but must not change these frozen derivation bytes. #429 supplied planning only.

### Provider extensibility

**CONDITIONAL PASS.** Compatible providers extend typed profile data/projection through the current marked lifecycle; executable connectors negotiate one feature while carrying the existing PCK semantic. The implementation must certify the live provider matrix and OpenRouter collision semantics.

### Verdict

**GO for implementation on today's completed lean-core baseline when the later split milestone is scheduled.** Do not reimplement the finished profile lifecycle, do not globally occupy `PlaneAttemptTransforms`, and do not weaken existing wire and PCK authority guards.
