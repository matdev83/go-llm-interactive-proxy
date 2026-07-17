# Release gates (Go core)

Normative criteria for merge-to-main and local pre-push checks. Commands assume repo root.

## Summary

| Gate | Criterion | Command |
|------|-----------|---------|
| Conformance | 100% of matrix tests in `internal/testkit/conformance` pass | `make parity-checks` (same as `go test -parallel=8 -tags=integration ./internal/testkit/conformance/...`; see [conformance-matrix-evidence.md](conformance-matrix-evidence.md)) |
| Race (Req. 14.6) | Full suite under race on Linux CI | `bash scripts/race-check.sh --strict` (CI); on Windows `make test-race` is a no-op (race disabled locally) |
| Critical fuzz (Req. 15.4 + design) | Bounded smoke for each listed `Fuzz*` below | `make test-fuzz` or `make release-gates` (see budgets) |
| Migration fixtures (Req. 15.13) | Exactly **3** golden JSON files under `testdata/migration/` with fixed names | Enforced by `TestMigrationGoldenFixtureInventory` in conformance; see [testdata/migration/README.md](../testdata/migration/README.md) |

## API parity (LLM surfaces)

Normative matrices and row IDs: [.kiro/specs/llm-api-parity/design.md](../.kiro/specs/llm-api-parity/design.md). A protocol may be marked **parity-ready** only when every matrix row for that protocol is `implemented` or explicitly `out_of_scope`, with automated evidence at the layers named in the spec.

- **Fast conformance slice:** `make parity-checks` runs `go test -parallel=8 -tags=integration ./internal/testkit/conformance/...` (includes `parity_*_test.go` anchors, `TestParitySuiteSourceFilesPresent`, and `TestParityMatrixCompleteness` when compiled with the integration tag).
- **Golden / parity evidence map:** [conformance-golden-coverage.md](conformance-golden-coverage.md) (migration JSON, parity file ownership, matrix context; kept in sync by `TestConformanceGoldenCoverageDocPresent`).
- **Matrix iteration ↔ test traceability:** [conformance-matrix-evidence.md](conformance-matrix-evidence.md) (which harness exercises each FE×BE row; integration-tagged sources vs default `make test`).
- **Specification bundle hub:** [spec-bundle-index.md](spec-bundle-index.md) — orchestration, continuity, routing, and hook-bus scenario registries (`SB-*` IDs) with precommit doc/source checks.
- **Full release gate** remains `make release-gates` (conformance + Tier-1 fuzz).

**Parity-ready checklist (before claiming a protocol row is green):**

1. `design.md` row status is `implemented` or explicitly `out_of_scope` / `wire_only` with a reason.
2. [refclient-spec-matrix.md](../.kiro/specs/go-core-reimplementation-v1/refclient-spec-matrix.md) and [refbackend-spec-matrix.md](../.kiro/specs/go-core-reimplementation-v1/refbackend-spec-matrix.md) cite the same tests or deferrals as `design.md` (no contradictions).
3. `make parity-checks` passes locally; CI runs the same packages with integration conformance sources enabled via `go test -parallel=8 -tags=precommit,integration ./...` (see `.github/workflows/qa.yml`).

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
| `FuzzHandleMessageStreamEventUnion` | `internal/plugins/backends/protocols/anthropicmessages` | Anthropic stream union → events |
| `FuzzToolInputSchemaParametersJSON` | `internal/plugins/backends/protocols/anthropicmessages` | Anthropic tool schema unmarshal |
| `FuzzHandleChatCompletionChunk` | `internal/plugins/backends/openailegacy` | Chat completion chunk → events |
| `FuzzBuildChatToolsParametersJSON` | `internal/plugins/backends/openailegacy` | Chat tools JSON unmarshal |
| `FuzzHandleGenerateContentResponse` | `internal/plugins/backends/protocols/geminigenerate` | Gemini response JSON → events |
| `FuzzBuildToolsParametersJSON` | `internal/plugins/backends/protocols/geminigenerate` | Gemini tool params unmarshal |
| `FuzzMessageToContentToolResultJSON` | `internal/plugins/backends/protocols/geminigenerate` | Tool result JSON in invoke |
| `FuzzAssistantPartsToContentBlocksJSON` | `internal/plugins/backends/bedrock` | Assistant JSON part → Converse blocks |
| `FuzzParseNDJSONLine` | `internal/plugins/backends/acp` | ACP NDJSON line mapping |
| `FuzzMapSessionUpdateToEvents` | `internal/plugins/backends/acp` | ACP session/update map |
| `FuzzMergeHandshakeProfileExtensions` | `internal/plugins/backends/acp` | Handshake extensions + session id |
| `FuzzHookMutationValidators` | `internal/core/hooks` | Post-hook call + event validation |
| `FuzzAcceptClientUserAgent` | `internal/core/identity` | User-Agent accept bounds / controls |
| `FuzzAcceptClientAppURL` | `internal/core/identity` | OpenRouter app URL accept shape |
| `FuzzAcceptClientAppTitle` | `internal/core/identity` | OpenRouter app title accept bounds |
| `FuzzValidateIdentityYAML` | `internal/core/identity` | Identity YAML decode + Validate |
| `FuzzCaptureClientUserAgent` | `internal/plugins/frontends/identitywire` | Frontend UA capture into invocation |
| `FuzzCompleteJSONSuffix` | `internal/core/toolcallrepair` | Append-only JSON suffix completion (ADR 0007) |
| `FuzzSchemaPreScanCompile` | `internal/core/toolcallrepair` | Offline schema pre-scan/compile bounds |
| `FuzzEngineRepair` | `internal/core/toolcallrepair` | Deterministic tool-call repair engine |
| `FuzzComputeAnchor` | `internal/plugins/features/reasoningpreservation` | Exact non-reasoning anchor hash stability (issue #157) |
| `FuzzDecodeConfig` | `internal/plugins/features/reasoningpreservation` | Feature YAML decode/validation bounds (issue #157) |

## Time budget

- Local default: `FUZZTIME=500ms` per target (wall time scales with the number of rows in the table above).
- CI: `.github/workflows/qa.yml` sets `FUZZTIME=6s` per target for `make test-fuzz` (raise over ad-hoc local smoke when validating merges).

## Fuzz seed corpus (committed)

Native fuzz loads extra seeds from **`testdata/fuzz/FuzzFunctionName/`** next to the **package under test** (same rule as `go test` `testdata/`). One file = one seed input: raw bytes for `[]byte` fuzz parameters, UTF-8 file body for `string` parameters.

- Index and format rules: [testdata/fuzz/README.md](../testdata/fuzz/README.md) (files must use the `go test fuzz v1` encoding, not raw JSON-only blobs).
- After long local runs, copy minimized or interesting inputs from the fuzz cache into the right `testdata/fuzz/FuzzName/` tree; keep files small and non-secret.

## Single entry point

- `make release-gates` — conformance package tests (with **`-tags=integration`** for full matrix/parity), then `make test-fuzz` (all Tier 1 targets). This target does **not** run the race detector; use `make test-race` locally on Linux/macOS or rely on CI (`bash scripts/race-check.sh --strict`; Windows skips race via `scripts/race-check.ps1`).
- Full QA remains `make qa` (quality + unit tests + lint + vuln). CI also runs race, lint, and vuln as separate steps (see `.github/workflows/qa.yml`).

## Dual-plane economics and concurrency (feature gates)

Normative completion gates for dual-plane metering / authority / concurrency (requirements **15.9**, **17.1–17.9**). These complement the Tier-1 table above; they do not replace `make release-gates`. Prefer existing harnesses over new protocol stacks.

| Gate | Criterion | Command / evidence |
|------|-----------|-------------------|
| Cross-protocol baseline (17.1, 17.3) | OpenAI Responses/legacy, Anthropic, Gemini FE×BE matrix remains green (dual-plane features default-off compatible) | `make parity-checks` |
| Shared checkpoint contract | Supported frontend `Operation` values share the same executor frontend-ingress checkpoint boundary/lifecycle | `go test ./internal/core/runtime/ -run SharedCheckpointAcrossFrontend` |
| Parallel race benches (16.6) | Parallel routing under authority with 2/4/8 legs | `go test ./internal/core/runtime/ -run '^$' -bench BenchmarkParallelRaceLegsAuthority` |
| Critical fuzz | Existing Tier-1 protocol/decode fuzz smoke (not dual-plane fact/correction fuzz) | `make test-fuzz` or `make release-gates` |
| Race (17.9) | Full suite under race on Linux CI | `bash scripts/race-check.sh --strict` (CI); Windows `make test-race` is a documented no-op |
| PostgreSQL direct runtime (9.9, 17.9) | Cross-instance durable authority, lease, and journal proofs through a direct/admin-capable endpoint | `make test-authority-postgres-direct` with `LIP_TEST_POSTGRES_DSN` |
| PostgreSQL migrations (18.9-18.10) | Explicit admin migration followed by read-only schema verification | `make test-postgres-migrations`; prefers `LIP_MIGRATION_POSTGRES_DSN`, then test admin/runtime DSNs |
| PostgreSQL aggregate | Migration, direct runtime, and pooled runtime gates all pass | `make test-authority-postgres` |
| PostgreSQL pooled runtime (18.14–18.16) | Dual-endpoint pooled contracts (`LIP_TEST_POSTGRES_ADMIN_DSN` + transaction-pooled `LIP_TEST_POSTGRES_DSN` with `LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1`). Proves migrate/open split and pooler-safe DML. | `make test-authority-postgres-pooled` (`LIP_REQUIRE_POSTGRES_POOLER=1` fails closed). |
| Migration fixtures (15.13 / 17.2) | Golden inventory under `testdata/migration/` | conformance `TestMigrationGoldenFixtureInventory` via `make parity-checks` |
| Enterprise panic / malformed isolation (15.9) | Injected request/attempt/concurrency providers map panic and unknown decision/lease kinds through fail-closed `ErrUnavailable` (advisory may degrade); release compensate panics are isolated | `go test ./internal/core/authoritycoord/ -run Isolates` |
| Privacy (17.5–17.6) | No raw bearer/API-key/header leakage in default authority evidence | `go test ./internal/core/usageauthority/app/ -run ProjectAuthorityEvidence` |
| Crash / cancel / late correction | Reconcile orphans, journal corrections, authority release on cancel paths | `go test ./internal/core/metering/reconcile/ ./internal/infra/metering/journalstore/ ./internal/core/runtime/ -run 'Reconcile\|Correction\|AuthorityRelease\|Cancel'` |
| Compatibility when disabled (17.1) | Default dogfood path with metering/authority off remains functional | `go run ./cmd/lipstd check-config --config config/examples/dogfood-local-stub.yaml` + `testdata/enterprise_module` |
| Explicit non-goals (17.8) | No web GUI, payments, invoices, tax, SSO/SAML/SCIM, CSP, or compression algorithms in this feature | design / requirements exclusions |
| Architecture | Enterprise module stays public-only | `go test ./internal/archtest/ -run EnterpriseModule` |

Do **not** claim Windows race-green without Linux/CI evidence. Do **not** claim PostgreSQL green without a recorded `LIP_TEST_POSTGRES_DSN` run. Do **not** treat `make parity-checks` as dual-plane checkpoint proof; that proof is the shared executor checkpoint test above.
