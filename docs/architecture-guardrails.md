# Architecture guardrails

This document complements [ADR 0001](adr/0001-registry-driven-composition.md) and [ADR 0005](adr/0005-architecture-guardrails-and-complexity-budgets.md). It explains why we enforce structural rules and where to update the numbers.

## Goals

- Keep `internal/core` free of concrete plugin implementations.
- Avoid hidden composition (`init()`-driven registration in the standard bundle path).
- Cap growth of the orchestration layers so the codebase does not drift into an oversized “god core”.

## Automated checks

| Check | Location |
| --- | --- |
| Non-test line budgets for key trees | [`internal/archtest/guardrails_test.go`](../internal/archtest/guardrails_test.go) |
| No `func init()` in `internal/pluginreg` and `cmd/lipstd` (non-test `.go` files) | same |
| Core does not import bundled plugins | [`internal/core/runtime/boundaries_test.go`](../internal/core/runtime/boundaries_test.go) (`TestCorePackagesDoNotDependOnConcretePluginPackages`) |

Circuit breaker behavior (what counts as failure, recovery) is documented in [`routing-health-circuit-breaker.md`](routing-health-circuit-breaker.md).

Run `go test ./internal/archtest/...` and full `go test ./...` (also invoked from `make quality-checks` / CI).

## Updating budgets

When a deliberate feature requires a larger core or composition layer, raise the limits in `guardrails_test.go` and record the rationale in ADR 0005 or a short note in the PR.
