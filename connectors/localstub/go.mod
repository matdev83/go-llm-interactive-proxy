module github.com/matdev83/go-llm-interactive-proxy/connectors/localstub

go 1.26.5

require (
	github.com/Microsoft/go-winio v0.6.2
	github.com/matdev83/go-llm-interactive-proxy v0.0.0
	google.golang.org/grpc v1.82.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

// Local development: replace points at the monorepo root so GOWORK=off module
// tests/builds resolve public pkg/api contracts. Published releases omit this
// replace and depend on a tagged github.com/matdev83/go-llm-interactive-proxy version
// (see release.yaml).
replace github.com/matdev83/go-llm-interactive-proxy => ../..
