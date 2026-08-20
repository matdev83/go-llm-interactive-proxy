---
name: golang-modernize
description: "Plan and apply evidence-based Go modernization for language, standard-library, test, and tooling changes. Use when upgrading a module toward Go 1.26, replacing deprecated APIs, adopting new library helpers, or reviewing modernization risk."
---

# Modernize Go deliberately

Modernization is a compatibility change, not a style sweep. Start from the module’s declared Go version, supported platforms, public API promises, and a measured workload.

## Workflow

1. Read `go.mod`, toolchain files, build tags, CI, and release/support policy. Confirm the installed toolchain and whether the change may alter language or standard-library semantics.
2. Establish focused tests, benchmarks, and compatibility checks before editing. Search callers and generated code; do not mechanically replace names across text.
3. Prefer standard-library APIs whose availability matches the module’s `go` line. Run `go fix` or a targeted analyzer only after reviewing the diff; inspect each automated rewrite.
4. Migrate in coherent, reviewable groups. Keep behavior, error identity, wire output, ordering, and allocation/latency assumptions under test.
5. Run `gofmt`, focused tests, `go vet`, and package/build checks. For performance changes, compare representative benchmark distributions with `benchstat`.

## Useful Go 1.26-era opportunities

Check whether `slices`, `maps`, `cmp`, `min`/`max`, range-over-integer, `any`, improved iterators, `testing.T.Context`, `testing/synctest`, `b.Loop`, and `testing` artifact support simplify code in the actual target module. Confirm the exact API in the installed toolchain before using it. Do not label older features as new: `httputil.ReverseProxy.Rewrite` has existed since Go 1.20, and `context.WithoutCancel` since Go 1.21.

Use `math/rand/v2` only after considering deterministic seeds, output compatibility, and the public behavior of the old generator. Use `os.Root` (where the module’s supported Go version permits) for directory-scoped file operations instead of inventing path-prefix security checks.

## PGO and toolchain changes

PGO profiles must come from the production-like binary and representative workload. Build the exact program with the profile, compare a baseline and optimized build under the same conditions, and keep the profile with the source/build process that produced it. Do not promise a fixed percentage gain and do not produce a generic profile from an unrelated benchmark.

Treat compiler, linter, dependency, and CI upgrades as supply-chain changes: review release notes, checksums, compatibility, and reproducibility. A modernization is complete only when tests and supported-platform builds pass and any behavior change is explicitly accepted. See [tooling and measurement](references/tooling.md) and [version notes](references/versions.md).
