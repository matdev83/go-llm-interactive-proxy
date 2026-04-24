# Release gates (Go core)

Normative criteria for merge-to-main and local pre-push checks. Commands assume repo root.

## Summary

| Gate | Criterion | Command |
|------|-----------|---------|
| Conformance | 100% of matrix tests in `internal/testkit/conformance` pass | `go test ./internal/testkit/conformance/...` |
| Race (Req. 14.6) | Full suite under race on Linux CI | `bash scripts/race-check.sh --strict` (CI); on Windows `make test-race` is a no-op (race disabled locally) |
| Critical fuzz (Req. 15.4 + design) | Bounded smoke for each listed `Fuzz*` below | `make test-fuzz` or `make release-gates` (see budgets) |
| Migration fixtures (Req. 15.13) | Exactly **3** golden JSON files under `testdata/migration/` with fixed names | Enforced by `TestMigrationGoldenFixtureInventory` in conformance; see [testdata/migration/README.md](../testdata/migration/README.md) |

## API parity (LLM surfaces)

Normative matrices and row IDs: [.kiro/specs/llm-api-parity/design.md](../.kiro/specs/llm-api-parity/design.md). A protocol may be marked **parity-ready** only when every matrix row for that protocol is `implemented` or explicitly `out_of_scope`, with automated evidence at the layers named in the spec.

- **Fast conformance slice:** `make parity-checks` runs `go test ./internal/testkit/conformance/...` (includes `parity_*_test.go` anchors, `TestParitySuiteSourceFilesPresent`, and `TestParityMatrixCompleteness`).
- **Full release gate** remains `make release-gates` (conformance + Tier-1 fuzz).

**Parity-ready checklist (before claiming a protocol row is green):**

1. `design.md` row status is `implemented` or explicitly `out_of_scope` / `wire_only` with a reason.
2. [refclient-spec-matrix.md](../.kiro/specs/go-core-reimplementation-v1/refclient-spec-matrix.md) and [refbackend-spec-matrix.md](../.kiro/specs/go-core-reimplementation-v1/refbackend-spec-matrix.md) cite the same tests or deferrals as `design.md` (no contradictions).
3. `make parity-checks` passes locally; CI runs it via the **Parity checks** step in `.github/workflows/qa.yml`.

## Fuzz tiers

**Tier 1 (release / CI):** explicit targets below (`make test-fuzz`). Each `go test -fuzz=...` uses a trailing `$` on the fuzz name regex so only one fuzz runs per package when multiple `Fuzz*` exist. CI runs each with the same `FUZZTIME` (default `500ms` locally; override e.g. `FUZZTIME=3s make release-gates`).

| Fuzz function | Package | Role |
|---------------|---------|------|
| `FuzzJSONRoundTrip` | `internal/testkit` | JSON normalize / compare helpers |
| `FuzzParseSelector` | `internal/core/routing` | Route selector parser (printable corpus) |
| `FuzzParseSelectorFromBytes` | `internal/core/routing` | Route selector parser (arbitrary bytes as string) |
| `FuzzDecodeCreateRequest` | `internal/plugins/frontends/openairesponses` | Responses API body + packed route selector |
| `FuzzDecodeMessageRequest` | `internal/plugins/frontends/anthropic` | Anthropic Messages decode |
| `FuzzDecodeGenerateContentRequest` | `internal/plugins/frontends/gemini` | Gemini generateContent decode |
| `FuzzDecodeChatRequest` | `internal/plugins/frontends/openailegacy` | Legacy chat decode |
| `FuzzWriteNonStreamJSON_toolArguments` | `internal/plugins/frontends/anthropic` | Encode path `json.Unmarshal` on tool args |
| `FuzzBuildGenerateContentResponse_toolJSON` | `internal/plugins/frontends/gemini` | Encode path tool JSON |
| `FuzzCallValidateJSON` | `pkg/lipapi` | Canonical `Call` JSON + `Validate` |
| `FuzzMergeRouteQueryGenerationOptions` | `pkg/lipapi` | Route query → generation options |
| `FuzzCollectWithLimitsProgram` | `pkg/lipapi` | Stream aggregation limits |
| `FuzzStableCallIdentity` | `internal/core/diag` | Stable trace helpers on decoded calls |
| `FuzzParamsForCall` | `internal/plugins/backends/openairesponses` | Canonical call → Responses params |
| `FuzzHandleResponseStreamUnion` | `internal/plugins/backends/openairesponses` | Responses SSE union → events |
| `FuzzBuildToolsParametersJSON` | `internal/plugins/backends/openairesponses` | Tool JSON schema unmarshal |
| `FuzzHandleMessageStreamEventUnion` | `internal/plugins/backends/anthropic` | Anthropic stream union → events |
| `FuzzToolInputSchemaParametersJSON` | `internal/plugins/backends/anthropic` | Anthropic tool schema unmarshal |
| `FuzzHandleChatCompletionChunk` | `internal/plugins/backends/openailegacy` | Chat completion chunk → events |
| `FuzzBuildChatToolsParametersJSON` | `internal/plugins/backends/openailegacy` | Chat tools JSON unmarshal |
| `FuzzHandleGenerateContentResponse` | `internal/plugins/backends/gemini` | Gemini response JSON → events |
| `FuzzBuildToolsParametersJSON` | `internal/plugins/backends/gemini` | Gemini tool params unmarshal |
| `FuzzMessageToContentToolResultJSON` | `internal/plugins/backends/gemini` | Tool result JSON in invoke |
| `FuzzAssistantPartsToContentBlocksJSON` | `internal/plugins/backends/bedrock` | Assistant JSON part → Converse blocks |
| `FuzzParseNDJSONLine` | `internal/plugins/backends/acp` | ACP NDJSON line mapping |
| `FuzzMapSessionUpdateToEvents` | `internal/plugins/backends/acp` | ACP session/update map |
| `FuzzMergeHandshakeProfileExtensions` | `internal/plugins/backends/acp` | Handshake extensions + session id |
| `FuzzHookMutationValidators` | `internal/core/hooks` | Post-hook call + event validation |

## Time budget

- Local default: `FUZZTIME=500ms` per target (wall time scales with the number of rows in the table above).
- CI: `.github/workflows/qa.yml` sets `FUZZTIME=6s` per target for `make test-fuzz` (raise over ad-hoc local smoke when validating merges).

## Fuzz seed corpus (committed)

Native fuzz loads extra seeds from **`testdata/fuzz/FuzzFunctionName/`** next to the **package under test** (same rule as `go test` `testdata/`). One file = one seed input: raw bytes for `[]byte` fuzz parameters, UTF-8 file body for `string` parameters.

- Index and format rules: [testdata/fuzz/README.md](../testdata/fuzz/README.md) (files must use the `go test fuzz v1` encoding, not raw JSON-only blobs).
- After long local runs, copy minimized or interesting inputs from the fuzz cache into the right `testdata/fuzz/FuzzName/` tree; keep files small and non-secret.

## Single entry point

- `make release-gates` — conformance package tests, then `make test-fuzz` (all Tier 1 targets). This target does **not** run the race detector; use `make test-race` locally on Linux/macOS or rely on CI (`bash scripts/race-check.sh --strict`; Windows skips race via `scripts/race-check.ps1`).
- Full QA remains `make qa` (quality + unit tests + lint + vuln). CI also runs race, lint, and vuln as separate steps (see `.github/workflows/qa.yml`).
