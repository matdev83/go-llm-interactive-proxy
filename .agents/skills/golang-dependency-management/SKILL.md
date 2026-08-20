---
name: golang-dependency-management
description: "Go modules and workspace hygiene: deliberate upgrades, MVS, sums, tools, audits, and reproducible dependency changes."
---

# Go dependency management

Use the module graph as a reviewed input to the build. Before adding or upgrading a dependency, establish why the standard library or an existing module is insufficient, inspect its license and maintenance, check advisories, and test the behavior that matters. Keep the change narrow; avoid changing unrelated modules during a feature fix.

## Inspect first

```sh
go version
go env GOMOD GOPROXY GOSUMDB
go list -m all
go mod graph
go mod why -m example.com/dependency
go list -m -u -json all
```

`go.mod` declares module requirements and constraints. `go.sum` records cryptographic checksums for module content that Go has downloaded; it is important review material but is not a lockfile that pins one complete dependency tree. Commit it when the repository tracks it (normally yes), and review additions/removals along with `go.mod`. `go mod verify` checks downloaded content against recorded hashes.

Go uses Minimal Version Selection: the selected version is the highest minimum required by the graph, not necessarily the newest available release. Major versions with breaking changes use a `/vN` module-path suffix for N >= 2.

## Add or upgrade deliberately

```sh
go get example.com/widget@v1.4.2
go mod tidy
go test ./...
go vet ./...
git diff -- go.mod go.sum
```

Prefer an explicit version or a repository-approved update range. `go get -u` can upgrade many direct and indirect requirements and may introduce API, behavioral, license, or vulnerability risk; it is not inherently safe. `go get -u=patch` narrows the range but still requires tests and review. Do not use `@latest` in a reproducibility-critical build or CI command without resolving and recording the version.

For every update, inspect release notes and the module's own tests, run the affected package and integration tests, and run a vulnerability scan:

```sh
govulncheck ./...
go list -m all
```

`govulncheck` findings need reachability and deployment-context review; absence of a finding is not proof that a package is safe.

## Tools

For Go versions supporting `tool` directives, declare executable tools in `go.mod` and invoke them through the module tool set:

```go
tool (
	golang.org/x/perf/cmd/benchstat
	golang.org/x/vuln/cmd/govulncheck
)
```

The exact `go get -tool` and `go tool` support depends on the module's Go version; verify with `go help mod` and the repository's `go` directive. For older modules, a documented versioned `go install module/cmd@version` or a tools module may be appropriate. Do not assume an unversioned `@latest` install is reproducible.

## Workspaces and replacements

Use `go.work` only when local development genuinely spans multiple modules. A workspace can hide missing published-module requirements and can make local builds differ from CI. Run CI with an explicit workspace policy (`GOWORK=off` when appropriate) and test each module independently before publishing. `go.work.sum` may be committed or ignored according to repository policy; ignoring it reduces churn but requires every developer and CI job to reproduce the same sums, while committing it improves workspace reproducibility. Do not present either choice as a Go requirement.

Keep `replace` directives for local development out of released modules unless the replacement is an intentional, reviewable dependency policy. Use `exclude` only to prevent a known bad version from selection, and use `retract` in a module you publish to withdraw versions with an explanation.

## Vendoring

`go mod vendor` copies selected source into `vendor/`; it can support offline or policy-controlled builds, but it increases repository size and update work. If vendoring is part of the release contract, run it after the graph is settled and verify with `go test -mod=vendor ./...`. Otherwise rely on the module cache/proxy and checksum database with an explicit network policy.

## Review checklist

- module path and major-version suffix are correct;
- direct versus indirect requirements are intentional;
- `go.mod`, `go.sum`, and optional `vendor/` changes are explainable;
- licenses, provenance, advisories, and transitive code are reviewed;
- tests, vet, and security scans pass for supported build tags/platforms;
- tool versions and CI actions are pinned where reproducibility matters;
- no credentials, private replace paths, or local workspace assumptions leak into the release.

References: [auditing](references/auditing.md), [automated updates](references/automated-updates.md), [conflicts](references/conflicts.md), [versioning](references/versioning.md), [workspaces](references/workspaces.md), and [visualization](references/visualization.md).

Related local skills: `golang-continuous-integration`, `golang-security`, `golang-lint`, and `golang-project-layout`.
