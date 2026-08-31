module github.com/matdev83/go-llm-interactive-proxy/connectors/codex

go 1.26.6

require (
	github.com/Microsoft/go-winio v0.6.2
	github.com/gorilla/websocket v1.5.3
	github.com/matdev83/go-llm-interactive-proxy v0.0.0
	github.com/matdev83/go-llm-interactive-proxy/connector-support/acp v0.0.0
	github.com/tiktoken-go/tokenizer v0.8.1
	google.golang.org/grpc v1.83.2
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/dlclark/regexp2/v2 v2.5.1 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

replace github.com/matdev83/go-llm-interactive-proxy => ../..

replace github.com/matdev83/go-llm-interactive-proxy/connector-support/acp => ../../connector-support/acp
