# Capability catalogs

This document covers two separate mechanisms: **plugin-local hosted-model catalogs** (OpenAI) and the optional **models.dev snapshot catalog** wired through `model_catalog` in configuration.

## OpenAI hosted-model catalog (plugin-local)

Model-aware capability narrowing for OpenAI-hosted backends lives in
[`internal/plugins/backends/openaicaps/caps.go`](../internal/plugins/backends/openaicaps/caps.go).

Rules:

- Expand `ForHostedModel` when a new **hosted** model family changes tool, vision, or streaming support.
- Add or extend a **unit test** in `internal/plugins/backends/openaicaps/caps_catalog_test.go` for each new branch so catalog drift fails CI.
- Non-OpenAI providers keep per-backend `ResolveCaps` in their plugin package (`internal/plugins/backends/<id>`).

## Models.dev snapshot catalog (`model_catalog`)

The standard runtime can load a **local JSON snapshot** derived from models.dev (or a compatible provider/model map), refresh it in the background, and use it for **pre-upstream** routing decisions together with operator overrides. Core policy lives in [`internal/core/modelcatalog`](../internal/core/modelcatalog/); HTTP fetch and filesystem cache live in [`internal/infra/modelcatalog/modelsdev`](../internal/infra/modelcatalog/modelsdev/). Wiring is composed in [`internal/infra/runtimebundle`](../internal/infra/runtimebundle/).

Operator rules:

- Default is **disabled** (`model_catalog.enabled: false`). No request-time network access is required when usage is enabled but external refresh is off: decisions use the file at `cache_path` only.
- **Refresh vs routing:** Background refresh replaces the active catalog snapshot atomically. Each routing decision reads the **current** active snapshot via [`CatalogRuntime`](../internal/core/modelcatalog/runtime.go) (no per-resolve file or network I/O). After a successful refresh, subsequent requests see the new metadata.
- Set `model_catalog.cache_path` when enabling usage or external updates. Set `source_url`, `update_interval`, and `external_updates_enabled` only if you want periodic background fetches.
- **Overrides** (`model_overrides`, `backend_model_overrides`) are merged at startup into the catalog resolver. Pair rows (`backend` + `model`) win over model-only rows. Each row may include optional fact fields (see commented examples in [`config/config.yaml`](../config/config.yaml)): boolean capability flags (`true` / `false`; omit for unknown) and positive integer token limits (`context_limit_tokens`, `input_limit_tokens`, `output_limit_tokens`).
- Diagnostics: when `diagnostics_path` is set, catalog status is served on that path (protected like other admin diagnostics). Response fields are **diagnostic**, not a stable public API.

### Trust and exposure (operators)

- **`model_catalog.source_url`:** Background refresh issues an outbound HTTP GET to this URL using the process-wide upstream client. Treat this as **configuration-controlled egress** (SSRF-shaped if an attacker can edit config). Prefer **HTTPS**, trusted mirrors, and restrict who can change YAML to the same trust zone as your network policy.
- **`diagnostics.shared_secret`:** When empty, [`WrapDiagnosticsProtect`](../internal/core/diag/auth.go) does **not** authenticate diagnostic routes (including catalog status and route trace). Rely on **localhost binding**, reverse-proxy auth, or set a non-empty shared secret for anything reachable from an untrusted network.
- **`model_catalog.cache_path`:** The path is **not** derived from HTTP requests; it is operator-supplied. On Unix, use a dedicated directory with sane permissions and be aware of symlink expectations on shared hosts—standard filesystem hygiene.

Optional **`model_catalog.fetch_timeout`:** When set to a positive Go duration and the fetch `context` has no deadline, catalog GETs apply an additional upper bound on wait time (defense in depth alongside the HTTP client timeout). See commented sample in [`config/config.yaml`](../config/config.yaml).

**Context size estimates (resume turns):** Context-limit filtering compares a conservative byte estimate of the canonical call to catalog/override limits. When the client presents a secure-session **resume token**, additional upstream transcript bytes are expected but are **not** yet merged into the estimate automatically—the estimator reports unavailable for that branch unless something attaches [`WithSessionSizeContribution`](../internal/core/modelcatalog/estimate.go) on the request context (future wiring from transcript accounting). That preserves Req 7.5 (no exclusion without an estimate).

Maintainers:

- Do not import provider SDKs or models.dev wire types from `internal/core/modelcatalog` or `pkg/lipapi`. Architecture tests under `internal/archtest` enforce import closure.
- Raw external schema types stay in `internal/infra/modelcatalog/modelsdev`; normalize into core `ModelFacts` only.
