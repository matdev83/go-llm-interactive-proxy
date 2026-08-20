---
name: golang-documentation
description: "Go documentation workflow for doc comments, examples, READMEs, contribution guides, changelogs, and release-facing API docs."
---

# Go documentation

Use this skill when documenting a Go package, application, or release. Documentation is part of the API: derive claims from code and tests, state version/platform constraints, and keep examples runnable. Do not publish source, examples, Playground snippets, telemetry, or private package metadata without explicit authorization and a disclosure review.

## Start with the audience

Identify whether the reader is an API consumer, operator, contributor, or maintainer. Inspect the public package names, exported symbols, commands, configuration, supported Go versions, and existing docs before writing. Prefer a short task-oriented page over a copied API dump. Keep project-specific policy in the repository docs rather than inventing universal Go rules.

## Doc comments and examples

- Every exported package, type, function, method, constant, and variable needs a useful comment beginning with its identifier when it is part of the public API.
- Explain behavior, invariants, ownership, cancellation, errors, concurrency, and compatibility—not the spelling of the declaration.
- Document nil, empty, zero, and invalid-input behavior when callers can observe it.
- Use `ExampleName` and `ExampleType_Method` tests for executable documentation. Keep examples deterministic, bounded, and free of credentials or private endpoints; include `// Output:` only for stable output.
- Run `go test ./...` and inspect rendered docs with `go doc` or a local documentation server. `go vet` checks examples and doc comments in relevant configurations.

## README and application docs

A useful README answers: what the project does, supported environments, a minimal install/run path, configuration, a small example, operational concerns, testing, and where to report issues. Keep commands copyable and verify them from a clean checkout. Explain defaults and failure modes; never put real secrets in snippets. For applications, document endpoints, auth, timeouts, data retention, migrations, health checks, metrics, and graceful shutdown.

Use [application](references/application.md) for command and service docs, [project docs](references/project-docs.md) for repository-level files, and the templates under [assets/templates](assets/templates/) only as editable starting points.

## Library and API documentation

Document the import path, supported Go versions, compatibility policy, main types, lifecycle, error contracts, and one complete example. Keep examples close to the package they demonstrate. If a package is private or contains customer/internal data, keep docs private; do not register it with public documentation services by default. Public publishing and Playground links require explicit approval, a license/privacy check, and confirmation that the code and dependencies are intended for disclosure.

See [library](references/library.md) and [code comments](references/code-comments.md) for API-specific checks.

## Release and maintenance docs

Changelogs should describe user-visible behavior, compatibility, migrations, security fixes, and known limitations. A contribution guide should explain prerequisites, formatting, tests, generated files, commit/PR expectations, and local checks. Keep `llms.txt` or machine-readable project summaries concise and generated from verified project facts.

When documenting release artifacts, derive the URL from the actual GoReleaser or release configuration and test it against a real release. Archive names often include project, version, OS, and architecture; do not hard-code a guessed filename. Prefer the release page or an exact generated asset URL when the naming template is configurable.

## Review checklist

1. Read code, tests, configuration, and release metadata before making a claim.
2. Mark experimental, platform-specific, and version-specific behavior.
3. Make examples compile and keep output stable.
4. Check links, code blocks, commands, and filenames from a clean checkout.
5. Run documentation tests and the relevant package tests.
6. Review secrets, personal data, private paths, and licensing before publication.

Related local skills: `golang-testing`, `golang-project-layout`, `golang-cli`, `golang-continuous-integration`, and `golang-naming`.
