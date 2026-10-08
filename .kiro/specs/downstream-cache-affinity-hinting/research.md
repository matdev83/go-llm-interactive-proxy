# Research & Brownfield Gap Analysis

Audited against `main` at `40d90d36` (2026-10-08). The feature is unimplemented. Core ownership closure (#598/#599/#600, `.kiro/specs/archive/core-feature-ownership-full-closure/`) and the marker-aware provider-profile lifecycle (#619/#622) are complete and are reused, not re-certified.

## Verified evidence

| Fact | Evidence |
|---|---|
| Candidate-aware attempt transforms already receive `BackendID`, `BackendPrefixes` and the authoritative `SessionView` | `pkg/lipsdk/request/attempt_transform.go:20-42`; `internal/core/runtime/executor_attempt_transform.go:28-52`; run per candidate at `executor_open_attempt.go:470-473` |
| Existing standard attempt transforms use order 0 | `pathvirtualization/outbound/attempt.go:63`; `reasoningpreservation/transform.go:37` |
| `PlaneAttemptTransforms` is canonical-required, so any occupant declines the large-payload wire path | `pkg/lipsdk/feature/plane_manifest.go:538-540`; `internal/core/largebody/authority_gate.go`, `eligibility.go` |
| Pure standard features register a factory; absent rows contribute nothing | `internal/standardplugins/standard_table.go:141-160`; `features_install.go` (`featureAgentLoopGuard`, `featurePartsNoop`); `feature_yaml.go:11` |
| Session IDs are 32 random bytes | `internal/core/securesession/app/ids.go:16,33-43` |
| No production stage after attempt transforms writes canonical PCK in the root module | `rg '\.PromptCacheKey *='`: only frontend decode, backend-plugin carrier conversions, and the Codex connector's own payload |
| PCK alias conflicts are already rejected by `Call` validation | `pkg/lipapi/call.go:249`; `semantic_extension.go:44` |
| Direct OpenAI Responses does not forward PCK | `internal/plugins/backends/openairesponses/invoke.go:39` (`ParamsForCall` has no PCK) |
| Direct OpenAI Chat does not forward PCK | `internal/plugins/backends/openailegacy/invoke.go:44` |
| OpenAI-compatible family reuses those serializers | `internal/plugins/backends/openaicompat/invoke.go:24,43` |
| SDK has typed fields for both | openai-go v3.66.0 `ChatCompletionNewParams.PromptCacheKey`, `responses.ResponseNewParams.PromptCacheKey` |
| OpenResponses protocol already forwards PCK | `internal/plugins/protocols/openresponses/encode.go:59-63` |
| Profiles have a bounded, family-checked quirk list | `internal/providerprofiles/schema.go:81-87,246-263,490-493` |
| Profile builders already receive the compiled profile from the marker-aware wrapper | `internal/standardplugins/provider_profile_binding.go:236-262,304-327` |
| Catalog has 141 rows; `fireworks` (Responses) and `mistral` (Chat) exist; `xai-responses`, `runinfra` absent | `internal/providerprofiles/catalog.json` |
| Codex connector derives its own PCK and ignores the canonical one | `connectors/codex/internal/codex/attempt.go:50`, `headers.go:84-121` |

## Simplification record

The previous SDD (Oct 8 rebaseline) had 11 requirement areas, ~70 acceptance criteria, an 820-line design and 41 tasks, against steering budgets of 5 / 25 / 300 / 12 (`.kiro/steering/delivery.md`). This revision cuts it to one slice. Concepts retired:

| Retired | Why it is not needed |
|---|---|
| Keyed HMAC, `DomainKeyDeriver` capability, secure-root closure in `runtimebundle`, featurehost key composition, NUL-free label rule, key domain | Session IDs are 256-bit random; unkeyed domain-separated SHA-256 is equally opaque. It also becomes stable across restarts and nodes, which the keyed design did not guarantee for memory stores. |
| `pkg/lipsdk/backendfeature`, `execbackend.Backend.Features`, `AttemptMeta.BackendFeatures` | A backend that forwards PCK already accepts explicit client PCK on that carrier; the generated value uses the same carrier. Gating lives in the serializer (direct OpenAI always, compatible family by profile quirk). |
| Backend-plugin feature `downstream_cache_affinity_v1`, feature-minor entry, dual host offer lists, adapter mapping | Connectors already receive PCK through the existing semantic carrier; each connector decides whether to forward. OpenRouter is deferred. |
| Typed `cache_affinity` schema (per-flavor struct, transport enum, wire name, max length, synthesis flag, 10 validation rules, `MinCacheAffinityValueLength` cross-test) | A profile has exactly one family, all V1 targets use the typed SDK `prompt_cache_key` field, and the 50-char value fits every documented bound. One quirk ID replaces the schema. |
| Feature observer, `Source`/`Outcome` enums, metrics adapter in featurehost | Delivery steering defers metrics beyond a few counters; V1 ships none. Provider cache-usage evidence already exists. |
| featurehost adapter | The feature has no process resources, so it is a plain `StandardBundle().Features` factory. |
| Re-certification of the provider-profile lifecycle, marker-forgery tests | Already shipped and tested by #619/#622 (`ExpandProviderProfileRowsWithCatalog` rejects forged markers at `provider_profile_binding.go:153-157`). |
| Post-transform PCK writer AST ratchet, 12 bespoke arch ratchets, TCK package, benchmarks, ownership census | `testing.md` Test Proportionality: feature tests prove feature behavior; new arch checks extend generic rules in their own PR. The PCK-writer premise is checked once by the end-to-end test. |
| xAI header/Responses rows, RunInfra row, OpenRouter body precedence | Separate carriers or connectors; deferred as ordinary follow-ups. |

Net effect: zero changes to core, SDK, runtimebundle, featurehost and backend-plugin protocol; one ~40-line feature package, three small serializer edits, one quirk, two catalog edits.

## Accepted trade-offs

- **Wire fast path.** When enabled, every request takes the canonical path because the plane is canonical-required. Operators opt in knowingly; wire-path synthesis is deferred.
- **No per-backend synthesis opt-out.** When enabled, every backend that forwards PCK receives the generated value. That set is small and explicit: direct OpenAI, quirked profiles, OpenResponses-compatible upstreams (whose protocol defines `prompt_cache_key`).
- **No length pre-check.** The 50-char value fits all targets. An over-long explicit client key is rejected by the provider before output, which is already an explicit failure.

## Provider references (recheck before enabling a row)

- OpenAI Responses / Chat `prompt_cache_key`: https://platform.openai.com/docs/api-reference/responses
- Mistral prompt caching: https://docs.mistral.ai/studio/conversations/advanced/prompt-caching
- Fireworks prompt caching: https://docs.fireworks.ai/guides/prompt-caching
- Deferred: xAI https://docs.x.ai/developers/advanced-api-usage/prompt-caching, OpenRouter https://openrouter.ai/blog/tutorials/prompt-caching-sticky-routing/
