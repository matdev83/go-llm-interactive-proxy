# Phase 3 review

- Verdict: APPROVED.
- Scope: parent tasks 3.1–3.4 only.
- Boundary: new pure normalization/replay packages, V2 aggregate path with explicit V1 bridge, bounded family TCK, tests and evidence. No SQL, runtime, provider, rating, billing binding or public host changes.

## Requirement review

- Token normalization implements inclusive and separate cache partitions, preserves missing/null/original evidence, retains cache-lifetime keys, and keeps included reasoning informational. The exact synthetic 11.6 calculation remains test-only.
- Reduction uses full component and source/subject/charge scope, supports delta/cumulative/gauge/replacement/correction semantics, preserves absent unrelated values, and keeps immutable input observations.
- Replay deduplicates exact same-identity payloads before graph validation, rejects changed payloads, excludes receipt time from semantic identity, and retains origin/acquisition distinctions. Ambiguous cumulative ties fail explicitly.
- Pending coverage/supersession references make snapshots incomplete and non-payable; graph validators reject invalid resolved graphs. V1 facts enter through the trusted one-way bridge without changing V1 identity.
- The reusable TCK exercises token partitioning, exact evidence, image input, audio/video output, a namespaced resource meter and unresolved aggregate coverage without a frontend-by-backend matrix.

## Fresh verification

- `go test -count=1 ./internal/core/metering/... ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/... ./internal/testkit/contract/metering` — PASS.
- `go test -count=1 -shuffle=on ./internal/core/metering/normalize ./internal/core/metering/replay ./internal/core/metering/aggregate ./internal/testkit/contract/metering` — PASS.
- `go vet ./internal/core/metering/normalize ./internal/core/metering/replay ./internal/core/metering/aggregate ./internal/testkit/contract/metering` — PASS.
- `gofmt -l` over all owned Go files — no output.

The worker's initial RED was compile-time with behavior-first assertions already present. That is recorded accurately in the execution evidence and is not used as stronger evidence than it provides. No requirement-blocking finding remains.
