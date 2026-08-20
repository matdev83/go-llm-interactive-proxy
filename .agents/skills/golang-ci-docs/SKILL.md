---
name: golang-ci-docs
description: Design, maintain, and review Go continuous integration workflows (GitHub Actions, matrix testing, SHA pinning) and comprehensive code documentation (godoc, executable examples, ADRs).
---

# Go CI, Automation & Documentation Guide

Reliable Go software requires automated quality gates in CI and clear, standard documentation that integrates with the Go toolchain and `pkgsite`.

---

## 1. Continuous Integration & Workflow Best Practices

### Secure GitHub Actions Workflow
- **Pin Actions to Immutable Commit SHAs**: Prevent supply-chain poisoning by referencing full commit SHAs rather than mutable tags.
- **Enable Dependency & Build Caching**: Use native `actions/setup-go` caching for faster CI cycles.
- **Matrix Testing**: Test across OS platforms and Go minor versions when building portable software.

~~~yaml
# .github/workflows/ci.yml
name: CI
on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4.2.2
      
      - name: Set up Go
        uses: actions/setup-go@3041df56c16aff9da48da3f4cbe55ab85bc31221 # v5.2.0
        with:
          go-version: '1.24'
          check-latest: true
          cache: true

      - name: Verify Dependencies
        run: |
          go mod tidy
          git diff --exit-code go.mod go.sum

      - name: Run Linters
        uses: golangci/golangci-lint-action@971e284b6050e8a5849b72094c50ab08da042db8 # v6.1.1

      - name: Vulnerability Scan
        run: go run golang.org/x/vuln/cmd/govulncheck@latest ./...

      - name: Run Tests with Race Detector
        run: go test -v -race -coverprofile=coverage.txt -covermode=atomic ./...
~~~

---

## 2. Go Documentation Standards & Godoc

### Package Doc Comments
Every package should have a package comment immediately preceding `package name` (in `doc.go` or the primary package file):
~~~go
// Package lipapi defines the canonical request, event, capability,
// and error contracts for universal LLM translation and routing.
package lipapi
~~~

### Exported Identifier Comments
Every exported function, type, constant, and variable must start with its own name and form a complete sentence:
~~~go
// StreamCollector accumulates canonical stream events into a final response.
type StreamCollector struct { ... }

// NewCollector returns an initialized StreamCollector with default capacity.
func NewCollector() *StreamCollector { ... }

// Deprecated: Use NewCollector instead.
func CreateCollector() *StreamCollector { ... }
~~~

### Executable Testable Examples
Add examples in `_test.go` files that serve as verified documentation and run as part of `go test`:

~~~go
func ExampleStreamCollector_Collect() {
    collector := NewCollector()
    collector.AddEvent(Event{Type: "chunk", Content: "Hello"})
    collector.AddEvent(Event{Type: "chunk", Content: " World"})

    resp := collector.Finalize()
    fmt.Println(resp.Content)

    // Output:
    // Hello World
}
~~~

---

## 3. Architecture Decision Records (ADRs)

Document significant architectural choices under `docs/adr/` using a standard format:
1. **Title & Status**: Context, Proposed, Accepted, Superceded.
2. **Context**: The problem, constraints, and driving factors.
3. **Decision**: The chosen technical architecture and boundary design.
4. **Consequences**: Trade-offs, benefits, and maintenance considerations.
