module github.com/matdev83/go-llm-interactive-proxy/connectors/openrouter

go 1.26.6

require (
	github.com/Microsoft/go-winio v0.6.2
	github.com/matdev83/go-llm-interactive-proxy v0.0.0
	github.com/matdev83/go-llm-interactive-proxy/connector-support/openaicompat v0.0.0
	google.golang.org/grpc v1.84.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260819154853-08b0e4226688 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/matdev83/go-llm-interactive-proxy => ../..

replace github.com/matdev83/go-llm-interactive-proxy/connector-support/openaicompat => ../../connector-support/openaicompat
