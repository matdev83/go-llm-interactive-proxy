---
title: Repository Hardening and Release Automation
status: active
updated: 2026-07-29
---

# Repository Hardening and Release Automation

## Repository controls

- `main` is protected with strict status checks, admin enforcement, linear history, and required conversation resolution.
- Required checks: `Repo hygiene`, `Test (ubuntu-latest)`, `Test (windows-latest)`, `Test (macos-latest)`, `Analyze (Go)`, and `Go vulnerability check`.
- Force pushes and branch deletion are disabled.
- GitHub Actions default token is read-only; workflows declare least-privilege permissions.
- Third-party Actions are pinned to full 40-character commit SHAs with release-tag comments; checkout uses `persist-credentials: false`.
- Private vulnerability reporting, secret scanning, push protection, validity checks, and automated security updates are enabled.

## Release hygiene

- `.release-files` is the authoritative tracked release-file allowlist.
- `scripts/check-release-clean.sh` validates working tree, staged changes, or a Git ref.
- GoReleaser builds `lipstd` for Linux/macOS/Windows amd64/arm64, emits archives, SHA-256 checksums, and attestations.
- Production tags/releases require explicit owner approval; repository automation alone does not authorize publication.
- No repository license was added because owner approval was not supplied.

## CI topology

- PR QA is a focused Ubuntu gate for formatting/module consistency, architecture tests, vet, and CLI tests.
- The OS matrix runs portable public packages and release-binary builds. CLI integration remains on Ubuntu because hosted Windows executes administratively and macOS local-stub activation requires an unsupported secure channel in that test setup.
- Dedicated platform-smoke, process-tree, and backend cross-platform workflows retain native OS evidence.
- Heavy backend release gates, broader races, fuzzing, benchmarks, and modernization run on schedules or explicit dispatch rather than every PR.
- Workflow concurrency cancels stale branch/PR runs to control runner consumption.

## Dependency security

- Cursor SDK bridge uses an npm override to force transitive `undici` to `6.27.0`.
- This resolved nine advisories spanning high, moderate, and low severity; post-merge GitHub Dependabot open-alert count was zero.
- Minimal validation for lockfile-only security updates: `npm audit`, TypeScript typecheck/build, repository CI, and protected-branch merge.
