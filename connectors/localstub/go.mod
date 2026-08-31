module github.com/matdev83/go-llm-interactive-proxy/connectors/localstub

go 1.26.6

require (
	github.com/Microsoft/go-winio v0.6.2
	github.com/matdev83/go-llm-interactive-proxy v0.0.0
	google.golang.org/grpc v1.83.2
	gopkg.in/yaml.v3 v3.0.1
)

require (
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

// Local development: replace points at the monorepo root so GOWORK=off module
// tests/builds resolve public pkg/api contracts. Published releases omit this
// replace and depend on a tagged github.com/matdev83/go-llm-interactive-proxy version
// (see release.yaml).
replace github.com/matdev83/go-llm-interactive-proxy => ../..
