---
name: golang-popular-libraries
description: Select and evaluate Go libraries by fit, maintenance, API stability, security, licensing, and integration cost, with the standard library as the baseline.
---

# Go library selection

Library popularity is not a correctness signal. Start with the standard library and the module's existing dependencies. Add a dependency when it removes meaningful risk or complexity and its maintenance, license, transitive graph, and operational behavior are acceptable.

Before recommending a package, verify its current module path, latest compatible release, supported Go versions, license, repository activity, security advisories, and the API in the exact version the project will pin. Do not infer a maintainer or lifecycle guarantee from a README badge.

## Practical evaluation

Compare candidates against the actual constraint:

- standard-library capability and missing behavior;
- API fit and whether context/cancellation are supported;
- failure, retry, timeout, and resource-ownership semantics;
- compatibility with the project's Go version and build targets;
- transitive dependencies, generated code, CGO, and binary size;
- release cadence, issue responsiveness, license, and known vulnerabilities;
- migration and exit cost.

Prefer a small, direct dependency to an abstraction stack. Keep third-party calls behind a narrow adapter when the library is volatile or external. Pin versions through normal Go module review; run go mod tidy and tests after changes. Never use go get -u as an unreviewed bulk upgrade.

## Useful baselines

- HTTP: net/http, httptest, and standard URL/encoding packages cover many clients and servers.
- JSON: encoding/json is the stable baseline. Do not use old golang.org/x/exp/json guidance as if it were a released encoding/json/v2 API; verify the target Go release and module path.
- Logging: log/slog with a handler selected for the deployment.
- SQL: database/sql plus a driver; sqlx or pgx when their explicit features justify them.
- RPC: google.golang.org/grpc or net/http according to the wire contract.
- Tests: testing, fuzzing, and benchmark support; testify is optional.
- CLI: flag for small tools, then a command framework when command-tree features justify it.
- Schema/migrations: use the project's reviewed migration tool and driver conventions.

Examples in this skill are categories, not a mandated stack. Check pkg.go.dev and the upstream repository for exact symbols, versions, and security notices before writing code.
