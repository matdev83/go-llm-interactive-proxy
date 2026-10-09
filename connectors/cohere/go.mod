module github.com/matdev83/go-llm-interactive-proxy/connectors/cohere

go 1.26.6

replace github.com/matdev83/go-llm-interactive-proxy => ../..

require (
	github.com/Microsoft/go-winio v0.6.3
	github.com/matdev83/go-llm-interactive-proxy v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.84.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
