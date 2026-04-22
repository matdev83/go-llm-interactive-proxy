# Safety-related static analysis

This document describes how defensive correctness checks are layered in CI and locally.

## Tier 1 — mandatory (`.golangci.yml`)

The default `golangci-lint` configuration (also run in `.github/workflows/qa.yml`) enables:

| Linter | Role |
|--------|------|
| `govet` | Standard Go correctness checks |
| `staticcheck` | Broader bug and API misuse detection |
| `ineffassign` | Assignments with no effect |
| `misspell` | Obvious spelling mistakes in identifiers/strings |
| `revive` | Style and a subset of correctness rules (see `.golangci.yml` for disabled revive rules) |
| `forcetypeassert` | Disallows bare `x.(T)` assertions that can panic |
| `errcheck` | Flags unchecked errors from a conservative set of calls (e.g. `Close`) |
| `gofumpt` | Stricter formatting than `gofmt` (drop-in for style consistency) |

Run locally (same as CI):

```bash
golangci-lint run ./...
```

Or via `make qa` / `make lint` per `Makefile`.

## Tier 2 — optional deeper scans (not in default CI)

These tools are useful for security-adjacent or experimental analysis but tend to be noisy or slow. Run them manually when changing risky areas (crypto, auth, casts, `unsafe`).

### gosec

[securego/gosec](https://github.com/securego/gosec) flags common security issues. Example (from repository root after installing `gosec`):

```bash
gosec -tests ./...
```

The optional workflow [`.github/workflows/optional-gosec.yml`](../.github/workflows/optional-gosec.yml) runs a full medium-severity scan and a **separate G115-only** job (integer overflow / unsafe conversions) on a schedule; both jobs use `continue-on-error` until triaged.

Start with a narrow rule set if noise is high, e.g. integer conversion checks only:

```bash
gosec -include=G115 -tests ./...
```

Tune excludes (`-exclude`, `-exclude-dir`) for test-only patterns as needed.

### nilaway

[uber-go/nilaway](https://github.com/uber-go/nilaway) is a nilability analyzer. It can report false positives on generic or reflection-heavy code. Typical usage:

```bash
go install go.uber.org/nilaway/cmd/nilaway@latest
nilaway ./...
```

Treat reports as advisory until the team agrees on suppression conventions.

## Tier 3 — follow-ups

- `gocritic` and `wsl` were evaluated for Tier 1 but produce broad churn on this codebase; run them locally or promote selected checks after triage.
- If a rule proves stable after manual triage, consider promoting it into Tier 1 via `.golangci.yml` (possibly scoped with `issues.exclude-rules`).
- Optional scheduled workflows (e.g. monthly `workflow_dispatch` + `gosec`) can be added without gating merges; keep them `continue-on-error` until signal-to-noise is acceptable.
