# Bundled capability catalogs

Model-aware capability narrowing for OpenAI-hosted backends lives in
[`internal/plugins/backends/openaicaps/caps.go`](../internal/plugins/backends/openaicaps/caps.go).

Rules:

- Expand `ForHostedModel` when a new **hosted** model family changes tool, vision, or streaming support.
- Add or extend a **unit test** in `internal/plugins/backends/openaicaps/caps_catalog_test.go` for each new branch so catalog drift fails CI.
- Non-OpenAI providers keep per-backend `ResolveCaps` in their plugin package (`internal/plugins/backends/<id>`).
