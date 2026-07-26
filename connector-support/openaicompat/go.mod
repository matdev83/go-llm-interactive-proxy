module github.com/matdev83/go-llm-interactive-proxy/connector-support/openaicompat

go 1.26.5

require github.com/matdev83/go-llm-interactive-proxy v0.0.0

require (
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.81.1 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// Local development: replace points at the monorepo root so GOWORK=off module
// tests/builds resolve public pkg contracts. Published releases omit this
// replace and depend on a tagged github.com/matdev83/go-llm-interactive-proxy version.
replace github.com/matdev83/go-llm-interactive-proxy => ../..
