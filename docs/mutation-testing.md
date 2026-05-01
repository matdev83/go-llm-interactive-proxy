# Mutation testing (optional)

Line coverage alone does not prove tests catch regressions. **Mutation testing** introduces small semantic changes (“mutants”) into production code; if the suite still passes, the mutant may indicate **missing assertions** or **weak tests**.

Recommended targets in this repo (high leverage, bounded runtime):

- [`pkg/lipapi`](../pkg/lipapi/) — validation, negotiation, collectors.
- [`internal/core/routing`](../internal/core/routing/) — selector parser and alias behavior.

## Tooling

Practical options:

1. **Gremlins** — [github.com/go-gremlins/gremlins](https://github.com/go-gremlins/gremlins): Go-oriented mutation operators; run locally against a package path with configurable timeouts.
2. **Manual discipline** — Pair critical fixes with **regression tests** and **golden** updates per [.kiro/steering/testing.md](../.kiro/steering/testing.md).

**Pinned release:** install the same version CI and scripts use so flags and behavior stay aligned:

```bash
go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0
```

Example runs:

```bash
gremlins unleash --path ./pkg/lipapi --timeout 5m
gremlins unleash --path ./internal/core/routing --timeout 5m
```

### Exit codes (Gremlins)

Gremlins follows usual CLI conventions: **0** when the run finishes successfully; **non-zero** when the command aborts (for example invalid flags, build/test infrastructure failure, or internal error). Mutation outcomes (**KILLED**, **LIVED**, etc.) are reported in the tool output; turning the run into a strict quality gate (fail when any mutant **LIVED**) is configuration-dependent—see [Gremlins documentation](https://gremlins.dev/) for the version you installed.

Repo scripts (same targets; exit **0** when `gremlins` is not installed—smoke-only):

- [`scripts/mutation-smoke.sh`](../scripts/mutation-smoke.sh) (Linux/macOS/Git Bash)
- [`scripts/mutation-smoke.ps1`](../scripts/mutation-smoke.ps1) (Windows)

Optional manual GitHub Actions workflow: [`.github/workflows/mutation-weekly.yml`](../.github/workflows/mutation-weekly.yml) (`workflow_dispatch` only).

CI does **not** run mutation tests by default; use them periodically or before large refactors of canonical types or routing.
