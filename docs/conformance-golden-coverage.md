# Conformance and golden coverage map

This document is the human-readable companion to automated checks in `internal/testkit/conformance/`. It maps **bundled protocol surfaces** to **evidence types**: parity suite sources, Python→Go migration goldens, and matrix-oriented conformance tests.

Normative parity matrices and row IDs: [.kiro/specs/archive/llm-api-parity/design.md](../.kiro/specs/archive/llm-api-parity/design.md). **FE×BE cell ↔ test mapping:** [conformance-matrix-evidence.md](conformance-matrix-evidence.md). Release gate inventory for migration JSON: [release-gates.md](release-gates.md).

## Evidence types

| Kind | Purpose |
|------|---------|
| **Parity suite** (`parity_*_test.go`) | Reference-client ↔ frontend ↔ core ↔ bundled backend ↔ refbackend or stubs; protocol-faithful cells per design matrix. |
| **Migration goldens** (`testdata/migration/*.json`) | Anchored wire snapshots from the Python LIP lineage (Req. 15.13); not the full protocol surface but regression anchors for cross-implementation shape. |
| **FE×BE matrix** | `matrix.go` / `matrix_test.go` enumerates bundled frontend × backend cells with subset metadata (e.g. ACP tools posture). |
| **Conformance scenarios** | `conformance_*_test.go` — text, tools, multimodal, credentials, streaming auth — exercise composed wiring beyond single-protocol parity files. |

## Parity suite files (by protocol id)

Each bundled protocol id must appear in `ParityProtocolEvidence` and have at least one listed source file present on disk (`release_gates_test.go` / `parity_evidence.go`).

| Protocol id | Parity suite source file(s) |
|-------------|----------------------------|
| `openai-responses` | `parity_openai_test.go` |
| `openai-legacy` | `parity_openai_test.go` |
| `anthropic` | `parity_anthropic_test.go` |
| `gemini` | `parity_gemini_test.go` |
| `bedrock` | `parity_bedrock_test.go` |
| `acp` | `parity_acp_test.go` |

## Migration golden JSON (testdata/migration)

Fixed inventory (exactly three files for Req. 15.13):

- `python_lip_anthropic_messages_nonstream.json`
- `python_lip_openai_responses_http_nonstream.json`
- `python_lip_openai_responses_http_streaming.json`

See [testdata/migration/README.md](../testdata/migration/README.md) for provenance.

## When extending coverage

1. Add or adjust parity tests in the appropriate `parity_*_test.go` and update `ParityProtocolEvidence` / this table.
2. Add matrix subset metadata in `matrix.go` when a FE×BE cell has intentional limits.
3. New migration snapshots require updating `ExpectedMigrationGoldenJSON`, `docs/release-gates.md`, and [testdata/migration/README.md](../testdata/migration/README.md).
