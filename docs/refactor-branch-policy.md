# Branch and feature-flag policy (maintainers)

Large refactors that touch orchestration, routing, or public `pkg/` contracts should land through a **short-lived integration branch** (for example `stage-two/<topic>`) rather than mixed into unrelated fixes on `main`.

## When to use a branch

- Registry or composition-root moves that require coordinated updates across `cmd/`, `internal/stdhttp/`, and `internal/pluginreg/`.
- Changes that temporarily break the conformance suite until follow-up commits land.

## Feature flags

- Prefer **compile-time** bundle tags only when necessary for cross-repo experiments; the standard distribution defaults to full bundled registration.
- Runtime toggles belong in **configuration** (`config.yaml`), not environment-only surprises, unless documented for operators.

## Review expectations

- Link the relevant Kiro spec task ids in the PR description.
- Run `make test` (or repository default CI gate) before merge; do not merge with known failing matrix tests introduced by the same series.
