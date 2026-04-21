# Python LIP to Go proxy: routing and capability migration notes

This document summarizes semantic differences operators should expect when moving from the Python `llm-interactive-proxy` to the Go `go-llm-interactive-proxy` core.

## Routing

- Selectors use the same string-oriented failover syntax (`a:model|b:model`, weighted branches, `[first]` steering). `routing.max_attempts` caps **B-leg opens** (initial open plus recv-phase replacements) per logical request.
- Model-only selectors require an explicit `default_route` / default backend resolution path; unresolved model-only selectors fail deterministically instead of surfacing as unknown-backend surprises at open time.

## Capabilities

- Capability negotiation is **candidate-aware** for bundled backends: required features are checked against the resolved backend/model pair before upstream I/O. Rejects happen before streaming starts; downgrades are explicit and attempt-local relative to the immutable client baseline.

### Capability catalog surface (Python vs Go)

| Concern | Python `llm-interactive-proxy` | Go `go-llm-interactive-proxy` |
| --- | --- | --- |
| Where catalogs live | Connector/strategy modules and model services (scattered per provider) | Bundled backends: OpenAI-hosted narrowing in [`internal/plugins/backends/openaicaps`](../internal/plugins/backends/openaicaps); other providers in `internal/plugins/backends/<id>` via `ResolveCaps` |
| Negotiation timing | Varies by transport layer and controller | Single `lipapi.Negotiate` path in the executor **before** `Backend.Open` for each attempt |
| Downgrade vs reject | Policy depends on connector and middleware | Explicit `NegotiationDowngrade` applies attempt-local changes; hard missing caps surface as `ErrCapabilityReject` |
| Operator drift checks | Pytest + connector unit tests | Go tests on catalogs (e.g. `caps_catalog_test.go`) plus executor routing tests |

## Continuity

- In-memory and SQLite stores share the same composition entry (`pluginreg.OpenContinuityStore`). SQLite persists A-leg and attempt lineage across process restarts when configured.

## Diagnostics

- Optional JSON plugin inventory is available at the configured `diagnostics.inventory_path` (for example `/debug/inventory`) when diagnostics are enabled.

For protocol-level differences, prefer conformance tests under `internal/testkit/conformance` and golden fixtures under `testdata/`.
