# Refinement 7.3 root-module multimodal producer census

Status: `DONE READY_FOR_REVIEW_REFINEMENT_7_3_ROOT`

## Scope and method

This is the root-module census for refinement 7.3. It covers the standard
distribution's static builtins, built-in-compatible family factories, the
embedded provider-profile catalog, root protocol mappers, and feature paths
that can observe or preserve media. Executable modules under `connectors/` and
`connector-support/` are explicitly out of scope; they are not counted as root
producers here. No allocation, tariff/price inference, stream-time journal
write, connector edit, commit, or Kiro-status change was made for this task.
The other dirty paths visible in this shared worktree belong to the parent or
other refinement owners; this task changed only this evidence file.

The inventory was mechanically checked from
`internal/standardplugins/standard_contributions.go`,
`internal/providerprofiles/catalog.json`, the Phase 1 producer census, and
root-module media/capability/usage anchors. The root economic producer anchors
are the six provider-producer rows in the Phase 1 census (rows 16--21):
`openaiusage`, `openresponsescompat`, `anthropicmessages`, `geminigenerate`,
`bedrock`, and the test/reference `localstub` stream.

## Counts

| Surface | Count | Result |
| --- | ---: | --- |
| Standard backend registrations | 10 | 6 `SourceBuiltin` static backends and 4 `SourceBuiltinCompatible` family factories |
| Direct static builtins | 6 | OpenAI Responses, OpenAI legacy, Anthropic, Alibaba Token Plan Intl, Gemini, Bedrock |
| Built-in-compatible family factories | 4 | OpenAI Responses, OpenAI Chat, Anthropic, OpenResponses |
| Embedded provider profiles | 141 | 129 OpenAI Chat, 8 OpenAI Responses, 4 Anthropic |
| Profiles with `enable` entries | 0 | All profile capability changes are ceilings/disable lists |
| Profiles not disabling `vision` | 0 | Effective profile media vision capability is disabled for 141/141 rows |
| Profiles not disabling `documents` | 0 | Effective profile document capability is disabled for 141/141 rows |
| Root economic producer anchors | 6 | Five protocol/provider paths plus test-only localstub |

The family defaults are media-capable where the protocol supports vision and
documents, but `providerprofiles` compiles every catalog row with explicit
`vision` and `documents` disables. `standardplugins` applies the compiled
capability ceiling while binding profiles to an existing family adapter; no
catalog row silently upgrades a family adapter into an unqualified media
producer. `video_input` is an explicit OpenResponses-compatible declaration,
not a catalog default.

## Registration and producer disposition

The status below is deliberately scoped. “Certified” means the enumerated
provider-native fields are bridged with direction, native unit, presence, and
provider provenance. It does not turn an output reference or a missing native
field into a charge. Any field outside the enumerated provider surface remains
raw/absent and is explicitly unsupported rather than inferred.

| Root registration / producer | Media surface | Fixture-backed economic boundary | Disposition |
| --- | --- | --- | --- |
| `openai-responses` | OpenAI Responses image/file input and Responses output media references | `openaiusage.ProviderEvidenceDraft` and `NativeUsageMeasures` preserve image/audio/video/document/file native measures, input/output direction, fractional duration, discrete units, presence, raw paths, provider request context, and genuine provider-reported cost. `openairesponses` output-media fixtures and parent refinement-7.1 economics fixtures prove refs and usage are separate. | `v2-certified` recognized native bridge; unrecognized/absent advanced fields stay unsupported/raw, and refs alone do not invent usage or cost |
| `openai-legacy` | OpenAI Chat image/file input | The same `openaiusage` path is used by Chat completion events; legacy invocation fixtures prove image/file wire mapping. Native media measures are not converted to text tokens and provider cost is retained only when actually reported. | `v2-certified` recognized native bridge with bounded unsupported advanced coverage |
| `custom-openai-responses-compatible` | OpenAI Responses-compatible family image/file input | `openaicompat` response/chat streams call the shared `openaiusage` producer. Family binding is data-driven through `providerprofiles`; the native measure/raw-preservation tests cover the shared path. | `v2-certified` family bridge; no synthetic cost or native-unit fallback |
| `custom-openai-legacy-compatible` | OpenAI Chat-compatible family image/file input | Shared `openaicompat` Chat stream and invocation paths retain usage presence/raw provider context and use the same native-measure parser. | `v2-certified` family bridge; advanced fields not surfaced by a provider remain unsupported |
| `anthropic` | Anthropic Messages image/document input and assistant image/document references | `anthropicmessages` final-provider-boundary fixture decodes input image bytes while keeping canonical ingress bytes distinct; map-event fixtures cover assistant image/document refs. The pinned SDK usage surface supplies token/cache/reasoning/server-tool fields, not native media units. | `v2-certified` surfaced token/cache/reasoning/provider-boundary evidence; explicit unsupported advanced economic coverage for native media units, duration/resolution/storage/resource/price, with no inference |
| `alibaba-token-plan-intl` | Alibaba Token Plan Intl reuses the Anthropic Messages protocol path | `alibabatokenplanintl.New` delegates to `anthropicmessages.NewBackend`; therefore the same final provider-boundary and native-absence disposition applies. No second provider-local economic producer exists. | `v2-certified` shared Anthropic bridge plus explicit unsupported advanced media economics |
| `custom-anthropic-compatible` | Anthropic-compatible image/document input and assistant references | The compatible factory binds to `anthropic.LifecycleAnthropicCompatible`, which uses the same `anthropicmessages` provider evidence and final-boundary path. | `v2-certified` surfaced fields; explicit unsupported advanced media economics |
| `gemini` | Gemini image/audio/video/document/file input and file-data output references | `geminigenerate` fixtures preserve prompt/candidate/cache/grounded-tool modality token counters with input/output direction and native units; prepared-boundary fixtures preserve provider-bound bytes, duration, and frames without fabricating resolution. Output file references do not create storage or usage charges. | `v2-certified` for every currently surfaced native modality/plane; explicit unsupported duration/resolution/storage/resource/price where the provider does not report it |
| `bedrock` | Converse image/document input; no root audio/video economic surface | Bedrock invocation fixtures prove image and PDF document wire payloads. Converse metadata fixtures preserve token presence/explicit zero and cache TTL (`5m`/`1h`) through the provider-evidence buffer. Converse does not surface native modality/storage/resource/price fields. | `v2-certified` surfaced token/cache evidence; explicit unsupported advanced modality/storage/resource/price coverage |
| `custom-openresponses-compatible` | OpenResponses image/file input; video requires explicit `video_input`; assistant media refs are not accepted by default | Parent refinement-7.1 OpenResponses-compatible fixtures prove native-only usage, input/output direction, native units, actual provider paths, late correction/revision, and no economics from assistant refs. Config tests reject an unnegotiated `assistant_media_refs` claim and require explicit video declaration. | `v2-certified` negotiated native bridge; unsupported advanced capabilities fail closed rather than being silently claimed |
| `local-stub` (test/reference only) | No vision/documents/media capability; deterministic text/tool fixture | `localstub.New` exposes only streaming and optional tools and is not statically registered. Its Phase 1 producer row is a deterministic six-counter V1 fixture with no provider-native media fields. | `lossless-v1-bridge`; explicitly non-media and makes no advanced economic claim |

The ten standard registrations above collapse onto five production protocol
economic paths: shared OpenAI usage, OpenResponses-compatible usage,
Anthropic Messages usage, Gemini usage, and Bedrock usage. Alibaba and the
Anthropic-compatible factory are aliases over the certified Anthropic path;
the two OpenAI-compatible factories are aliases over the certified
`openaicompat`/`openaiusage` path. This avoids treating a family registration
as a new provider-local meter.

## Provider-profile disposition

`catalog.json` contains 141 rows: 129 `openai-chat-compatible`, 8
`openai-responses-compatible`, and 4 `anthropic-compatible`. A mechanical JSON
audit found `enable_nonempty=0`, `vision_not_disabled=0`, and
`documents_not_disabled=0`. `CompileProfile` preserves the profile data and
`standardplugins` applies the resulting capability ceiling when it binds a row
to its family factory. Consequently, all 141 catalog rows are negotiated as
non-media for vision/documents until a future profile explicitly changes the
catalog and supplies a corresponding fixture-backed economic disposition.

The generic OpenResponses-compatible config remains explicit at the ad-hoc
row level: vision/documents are representable, video input requires
`video_input`, and `assistant_media_refs` is rejected without a response-media
surface. No profile row currently bypasses those boundaries.

## Feature paths reviewed

These paths can see or preserve media-shaped canonical parts, but they are not
foreground provider-economic producers:

| Feature path | Media behavior | Economic disposition |
| --- | --- | --- |
| `internal/plugins/features/compactioncontinuity/source/{prepare,eligibility}.go` | Preparation consumes bounded text/decision/structured carriers and drops media/reasoning/code-dump content; `TestPrepare_dropsMediaReasoningAndCodeDump` is the fixture. | The eligibility result is a semantic cost gate only. It emits no provider usage, native unit, or price claim. |
| `internal/plugins/features/compactioncontinuity/plugin_response.go` and feature observability | Compaction release/capsule state is continuity metadata; observability is content-free. | Explicitly unsupported for a foreground native child-B-leg/resource economic subject; scheduler and billing remain owners. |
| `internal/plugins/features/reasoningpreservation/{anchor,observer,raw_extractor}.go` | Anchors include image/file reference identity; stream observer preserves assistant image/file refs; `ExtractBoundedRaw` rejects `Collected.AssistantMedia` before decode. | Semantic continuity only. Assistant media is not accepted as raw reasoning economics, and no provider usage/cost is emitted. |
| `internal/standardplugins/featurehost/keepwarm.go`, `internal/core/billing/maintenance.go` | Keep-warm renewal records carry a separate maintenance identity and token/cache counters. | `ProviderMaintenanceUsage` is native maintenance evidence, not foreground B-leg inference; cost is empty and no media economics are claimed. |

## Gaps and fixes

No root-module mapping gap was found. The existing root fixtures and the
parent refinement-7.1/7.2 fixtures cover the currently surfaced native
direction/unit/provider-path cases; Anthropic and Bedrock explicitly bound
their provider-native absence rather than inventing measures. Therefore this
task adds no Go fixture, mapping, capability, or catalog row. In particular,
there is no root producer that silently claims complete media economics:

* complete claims are limited to enumerated native fields with raw/provider
  provenance and native units;
* missing/advanced provider fields are absent or explicitly unsupported;
* media references are transport outputs, not usage or charges; and
* every embedded provider profile currently disables vision/documents.

## Verification

Fresh checks on the current worktree:

* `go test -count=1 ./internal/providerprofiles ./internal/standardplugins/...` — PASS
* focused root backend families (`openaiusage`, `openaicompat`,
  `openailegacy`, `openairesponses`, `openresponsescompat`,
  `protocols/anthropicmessages`, `protocols/geminigenerate`, `bedrock`,
  `anthropic`, `alibabatokenplanintl`, `gemini`) — PASS
* focused feature families (`compactioncontinuity/...`,
  `reasoningpreservation/...`) — PASS
* `go test -count=1 ./internal/testkit/conformance ./internal/testkit/compatibleparity ./internal/testkit/contract/...` — PASS
* `go vet` over the root provider/profile/standard-plugin/backend/feature
  families — PASS
* `go test -count=1 ./internal/qa` — PASS
* targeted `internal/archtest` producer/Phase8/provider-boundary/keep-warm
  tests — PASS
* `make parity-checks` — PASS (the repository target also ran its normal
  connector sub-jobs; no connector source was changed for this task)
* `gofmt -d` over all eleven dirty Go paths (including the parent delta's
  untracked fixtures) — no output; `git diff --check` — PASS; evidence-file
  trailing-whitespace check — PASS

Skipped by scope: connector implementation or fixture changes, price/tariff
inference, allocation, and race testing of unrelated parent/connector deltas.
