---
type: reference
title: Technology Stack
description: Go version, direct dependencies, tooling, and structural implementation patterns.
stack: [go]
tags: [go, dependencies, tooling]
status: active
---

# Technology Stack

## Language & Runtime

- **Go 1.26.x** (currently 1.26.4 per `go.mod`)
- Module: `github.com/matdev83/go-llm-interactive-proxy`

### Standard Library Core

| Dependency | Role |
|---|---|
| `net/http` | HTTP server |
| `log/slog` | Structured logging |
| `encoding/json` | Default serialization |
| `database/sql` | Persistence base |
| `testing`, `testing/fstest`, `testing/iotest` | Testing |
| `context` | Request lifecycle everywhere |

## Major Dependencies

### Provider SDKs (backend adapters only)

| SDK | Provider |
|---|---|
| `github.com/openai/openai-go/v3` | OpenAI |
| `github.com/anthropics/anthropic-sdk-go` | Anthropic |
| `google.golang.org/genai` | Google Gemini/GenAI |
| `github.com/aws/aws-sdk-go-v2/service/bedrockruntime` | AWS Bedrock |
| `github.com/aws/aws-sdk-go-v2/service/bedrock` | AWS Bedrock model listing |

### Observability

| Library | Role |
|---|---|
| `github.com/prometheus/client_golang` | Prometheus metrics |
| `go.opentelemetry.io/otel` + OTLP HTTP | OpenTelemetry tracing |
| `github.com/prometheus/client_model` | Metrics model |

### Persistence & Data

| Library | Role |
|---|---|
| `github.com/uptrace/bun` + dialects | ORM-adjacent query builder |
| `modernc.org/sqlite` | Pure-Go SQLite (no CGo) |
| `github.com/jellydator/ttlcache/v3` | In-memory TTL cache |

### Configuration & Serialization

| Library | Role |
|---|---|
| `gopkg.in/yaml.v3` | YAML config parsing |
| `github.com/tidwall/gjson` / `sjson` | JSON path operations |
| `github.com/invopop/jsonschema` | JSON schema generation |

### Other

| Library | Role |
|---|---|
| `github.com/tiktoken-go/tokenizer` | Token counting |
| `github.com/gorilla/websocket` | WebSocket support |
| `github.com/samber/slog-formatter` / `slog-multi` | Structured logging compositions |
| `go.uber.org/goleak` | Goroutine leak detection in tests |
| `golang.org/x/sync` | Concurrency utilities |

## Tooling

- **Linter:** `golangci-lint` v2 (`.golangci.yml`)
- **Vuln check:** `go tool govulncheck`
- **Build system:** Makefile-based (`Makefile`)
- **CI:** GitHub Actions — PR QA (`.github/workflows/qa.yml`, skips when no `*.go` changes); nightly race + fuzz (`.github/workflows/race-fuzz-nightly.yml`)
- **Pre-commit hooks:** `.githooks/`

## Structural Patterns

- Explicit construction/registration; no DI containers
- Interfaces defined where consumed, not in central `ports/` packages
- Constructors return concrete types (except stable SDK/plugin contracts)
- Compile-time interface assertions near implementations
- `internal/` packages for non-public code
- Forward-slash git pathspecs on Windows
