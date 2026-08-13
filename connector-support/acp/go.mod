module github.com/matdev83/go-llm-interactive-proxy/connector-support/acp

go 1.26.6

require (
	github.com/matdev83/go-llm-interactive-proxy v0.0.0
	go.uber.org/goleak v1.3.0
	golang.org/x/sync v0.22.0
)

// Local development: replace points at the monorepo root so GOWORK=off module
// tests/builds resolve public pkg contracts. Published releases omit this
// replace and depend on a tagged github.com/matdev83/go-llm-interactive-proxy version.
replace github.com/matdev83/go-llm-interactive-proxy => ../..
