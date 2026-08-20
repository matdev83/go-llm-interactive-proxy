---
name: golang-project-layout
description: "Go repository structure: right-sized packages, commands, internal visibility, modules, workspaces, tests, and configuration."
---

# Go project layout

Choose structure from the project's users, deployment units, and expected reuse. A small command may be one package; a service or library may need boundaries that can be tested independently. The directory names below are conventions and tools, not language requirements.

## Decide the boundaries

Identify the module or modules, public API, executable entry points, generated code, deployment artifacts, and test fixtures. Keep dependencies directed toward stable contracts and put orchestration at the application boundary. Add a package when it gives a useful ownership or API boundary, not merely to create more files.

Common choices:

| Need | Possible layout |
| --- | --- |
| One small command | `go.mod`, `main.go`, adjacent tests and docs. |
| Several commands | `cmd/<name>/main.go` plus shared packages; `cmd/` is a convention, not a requirement. |
| Reusable library | Root or named public package; `internal/` for implementation details. |
| Service | Command entry point, internal transport/application/domain packages, migrations/config as needed. |
| Multiple modules | Separate `go.mod` files and an optional `go.work` for local development. |

Keep `main` thin when there is substantial business logic: parse configuration, construct dependencies, run the application, and handle shutdown. A flat package is appropriate when that is clearer.

## Module and package rules

Choose a module path that matches how consumers fetch it when the module is published; private modules can use the organization's configured path. Lowercase import paths are conventional and major versions >=2 require a `/vN` suffix. Avoid a generic `utils` package and name packages for the domain they own. Package names normally match the directory's package declaration but need not mirror every repository folder.

`internal` visibility is defined by the parent directory tree: code can import an internal package only from within the tree rooted at its parent. It is not simply “private to the module.” `pkg/` is also a convention; use it only when it communicates a stable public boundary in this repository.

## Tests, fixtures, and generated files

Keep tests near the code they exercise. Any `_test.go` file is recognized by Go; names such as `_bench_test.go` or `_example_test.go` are optional organizational conventions. Benchmark and example function names (`BenchmarkX`, `ExampleX`) determine discovery. Keep external fixtures in `testdata/` when they should not be imported as packages, and clearly mark generated files and their command/version.

## Modules and workspaces

Use one module for one release/versioning boundary unless independent modules are genuinely needed. Use `go.work` to develop related modules together, but test each module with `GOWORK=off` before publishing so local replacements do not hide missing requirements. Document whether `go.work` and `go.work.sum` are committed or ignored according to repository policy.

## Configuration and repository files

Add only files the project uses. A `Makefile`, `.gitignore`, linter config, CI workflows, Dockerfile, and release config are useful when their commands are real and documented; none is required by Go. Keep configuration precedence explicit (defaults, config files, environment, flags, or application-specific overrides) and validate it at startup. See [config](references/config.md).

## Initialization checklist

- define the public API and deployment boundaries;
- choose a module path and Go version deliberately;
- keep commands and orchestration thin where that improves testing;
- add `internal` only for a real visibility boundary;
- place tests/fixtures/generated code predictably;
- document workspace and replacement behavior;
- run `gofmt`, `go test`, `go vet`, and repository-specific checks;
- avoid introducing a framework or DI library before its lifecycle and maintenance benefits are demonstrated.

See [directory layouts](references/directory-layouts.md), [testing layout](references/testing-layout.md), [workspaces](references/workspaces.md), and [configuration](references/config.md).

Related local skills: `golang-cli`, `golang-design-patterns`, `golang-dependency-management`, `golang-testing`, and `golang-code-style`.
