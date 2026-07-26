module github.com/matdev83/go-llm-interactive-proxy/connectors/codex

go 1.26.5

require (
	github.com/Microsoft/go-winio v0.6.2
	github.com/gorilla/websocket v1.5.3
	github.com/matdev83/go-llm-interactive-proxy v0.0.0
	github.com/matdev83/go-llm-interactive-proxy/connector-support/acp v0.0.0
	github.com/tiktoken-go/tokenizer v0.8.0
	google.golang.org/grpc v1.81.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/dlclark/regexp2/v2 v2.1.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

replace github.com/matdev83/go-llm-interactive-proxy => ../..

replace github.com/matdev83/go-llm-interactive-proxy/connector-support/acp => ../../connector-support/acp
