module github.com/matdev83/go-llm-interactive-proxy/connectors/nousportal

go 1.26.6

replace (
	github.com/matdev83/go-llm-interactive-proxy => ../..
	github.com/matdev83/go-llm-interactive-proxy/connector-support/oauthcred => ../../connector-support/oauthcred
	github.com/matdev83/go-llm-interactive-proxy/connector-support/openaicompat => ../../connector-support/openaicompat
)

require (
	github.com/Microsoft/go-winio v0.6.2
	github.com/matdev83/go-llm-interactive-proxy v0.0.0
	github.com/matdev83/go-llm-interactive-proxy/connector-support/oauthcred v0.0.0-00010101000000-000000000000
	github.com/matdev83/go-llm-interactive-proxy/connector-support/openaicompat v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.83.2
	gopkg.in/yaml.v3 v3.0.1
)

require (
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260819154853-08b0e4226688 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
