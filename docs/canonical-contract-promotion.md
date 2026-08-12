# Canonical Contract Promotion and Fidelity Audit

This document is the durable Phase 5 audit for protocol-named canonical fields. It
records ownership, evidence, and the rule for future additions. It is not a wire
protocol specification.

## Promotion Checklist

Before adding first-class state to `pkg/lipapi`, an author must answer all five
questions:

1. Which core policy or orchestration path consumes the semantic meaning?
2. Which independent protocol families share that meaning today?
3. Why can an existing bounded dialect/extension carrier not preserve residual
   adapter fidelity exactly?
4. Which projection, admission, accounting, hook, safety, continuity, or output
   commitment behavior depends on the field?
5. What public source and wire compatibility cost does promotion create, and what
   additive migration path is provided?

An unanswered question means the field remains adapter-owned or uses a negotiated
residual carrier. A carrier is not a request/response tunnel: it has bounded
identity, direction, presence, data, and exact requirement admission.

## Phase 5 Audit

| Field or carrier | Readers and writers | Classification | Decision |
| --- | --- | --- | --- |
| `Call.PromptCacheKey` | OpenResponses decoder/encoder, compatible backend request builder, Codex native context coordinator, backend-plugin conversion and ABI v1.3 | Adapter-only fidelity; core does not route, account, hook, or project its meaning | Retain source-compatible alias, but the negotiated `SemanticExtension` is the only carrier authority. Bridges validate exact equality and reject conflicts; legacy v1.3 remains authoritative only for legacy peers. |
| Reasoning `Summary` / `Content` presence | OpenResponses wire item decoder/encoder, reasoning feature/state machine, canonical `ReasoningPart`, backend-plugin DTO/conversion, stream event conversion | Shared/core-required reasoning replay state; exact JSON element vocabulary remains adapter-owned | Keep first-class on `ReasoningPart` and bounded `RawJSON` ABI fields. Presence is required for null-vs-omitted fidelity and reasoning replay. |
| Reasoning `EncryptedContent` presence | OpenResponses decoder/encoder, Codex response-item canonicalization, reasoning feature/state machine, backend-plugin DTO/conversion and events | Shared/core-required opaque reasoning continuity state | Keep first-class on `ReasoningPart` and event/ABI carriers. It crosses turns and must participate in exact reasoning admission. |
| Compaction `EncryptedContent` | OpenResponses compaction decoder/encoder, Codex native compaction, compaction projection/admission, backend-plugin item conversion | Shared/core-required compaction/replay state | Keep first-class on `CompactionItem`; it is interpreted by compaction/continuity policy and is bounded. |
| Inline `FileData` and extension content part | OpenResponses decoder/encoder and compatible backend conversion | Adapter fidelity carried by existing typed content/extension carriers | No new canonical residual needed; existing `ContentPart.FileData` and `ExtensionContentPart` already provide bounded, exact identity and admission. |
| Call-level residual metadata | Frontend/backend edge adapters and exact requirement derivation | Bounded adapter residual | `SemanticExtension` is the one generic presence-bearing residual carrier. Identity is lowercase bounded syntax with a closed request/response/bidirectional direction; values are bounded JSON values with request/response envelope keys rejected, and legacy projection rejects rather than tunnels. |

## Migration Evidence

Characterization coverage is in `pkg/lipapi/semantic_extension_test.go` and the
backend-plugin conversion/negotiation tests. It covers clone ownership, value/null
presence, bounds, closed identity/direction, duplicate identity, envelope rejection,
exact extension requirements, projection rejection, v1.0-v1.3 compatibility,
v1.5/v1.6 accounting-carrier separation, alias conflict rejection, unknown required
features, and old/new negotiated carrier behavior.
The existing OpenResponses/Codex round-trip suites remain the authority for the
first-class reasoning and compaction fields.

The reader/writer audit is concrete rather than inferred from DTO names:

- `PromptCacheKey`: `internal/plugins/frontends/openresponses/decode.go` writes the
  canonical field; `internal/plugins/protocols/openresponses/encode.go`,
  `internal/plugins/backends/openresponsescompat/request.go`, and the compact
  mapping tests read it; `connectors/codex/internal/codex/native_context_coordinator.go`
  and `native_context_store.go` use it for native-context identity; backend-plugin
  reads/writes are in `call_bridge.go`, `call_bridge_items.go`, `items_wire.go`,
  `invocation_meta.go`, `convert.go`, and `items_convert.go`.
- Reasoning `Summary`/`Content`/`EncryptedContent`: OpenResponses decode/encode and
  the reasoning state machine consume them; canonical storage is
  `pkg/lipapi/reasoning.go`; ABI conversion/validation is in
  `pkg/lipsdk/backendplugin/exact_reasoning_validate.go`, `call_bridge_items.go`,
  and `convert.go`; event conversion is covered by
  `exact_openresponses_roundtrip_test.go`.
- Compaction `EncryptedContent`: OpenResponses compaction and Codex native
  compaction read/write it; canonical projection/admission uses
  `pkg/lipapi/items.go`, `projection.go`, and `requirements.go`; ABI mapping is in
  `call_bridge_items.go` and `items_convert.go`.

The carrier audit found a real gap. `Call.Extensions` has only a string key and raw
JSON value, so it cannot preserve implementor, direction, or explicit null/value
identity as a negotiated requirement. Item/content `OpaqueExtension` carriers are
scoped to an item/content part and cannot carry a call-level residual. Existing
backend-plugin extension DTOs are likewise item/content scoped; `SafeMetadata` is
string-only and is not an exact semantic carrier. The v1.3
`exact_openresponses_fields` fields are protocol-specific legacy fields, not a
neutral call-level carrier. Therefore the single minor-6 carrier is justified;
reasoning and compaction fields remain first-class because core replay, continuity,
projection, and admission consume them.

The migration intentionally does not remove the source-compatible `PromptCacheKey`
alias or the reasoning and compaction fields. Removing shared reasoning/compaction
state would make replay or continuity lossy; removing the alias would needlessly
break source compatibility. The alias is not a second authority: on a carrier-aware
path it is only accepted when equal to the carrier value, and outbound conversion
emits one representation.
No raw request/response tunnel was added, and current connectors do not opt into
the new feature unless their negotiated offer includes it.

## Semantic ABI Evolution

`semantic_extensions_v1` is named for the canonical semantic carrier, not a
provider or protocol. It is additive at backend-plugin minor 6. `lip/prompt_cache_key`
is a reserved promotion of an adapter residual into this neutral carrier vocabulary,
not permission to add protocol-named ABI fields. The legacy
`exact_openresponses_fields` vocabulary remains accepted at minor 3 and is bridged
only where its existing authority is required; both vocabularies are never emitted
as competing authorities for the same residual value.

Positive examples:

- `semantic_extensions_v1` with namespace/type/implementor/direction/presence and
  bounded JSON data.
- A new neutral transport capability or canonical semantic feature with exact
  negotiation and a pre-output hard reject when unsupported.

Negative examples:

- `exact_anthropic_fields`, `gemini_thinking_v2`, or `openai_custom_schema` ABI
  feature names.
- `openai_prompt_cache_key` or `CodexCompactionWire` proto fields.
- A raw `bytes request_json` or `bytes response_json` field that bypasses routing,
  safety, hooks, accounting, projection, or output commitment.

Architecture tests reject new protocol/provider-named ABI vocabulary outside the
documented v1.3 compatibility allowlist. New protocol SDDs must attach this
checklist and evidence before promoting a canonical field.
