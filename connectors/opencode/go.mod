module github.com/matdev83/go-llm-interactive-proxy/connectors/opencode

go 1.26.5

require (
	github.com/Microsoft/go-winio v0.6.2
	github.com/matdev83/go-llm-interactive-proxy v0.0.0
	github.com/matdev83/go-llm-interactive-proxy/connector-support/openaicompat v0.0.0
	google.golang.org/grpc v1.81.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/puzpuzpuz/xsync/v3 v3.5.1 // indirect
	github.com/tmthrgd/go-hex v0.0.0-20190904060850-447a3041c3bc // indirect
	github.com/uptrace/bun v1.2.18 // indirect
	github.com/uptrace/bun/dialect/pgdialect v1.2.18 // indirect
	github.com/uptrace/bun/dialect/sqlitedialect v1.2.18 // indirect
	github.com/uptrace/bun/driver/pgdriver v1.2.18 // indirect
	github.com/vmihailenco/msgpack/v5 v5.4.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/metric v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	mellium.im/sasl v0.3.2 // indirect
)

replace github.com/matdev83/go-llm-interactive-proxy => ../..

replace github.com/matdev83/go-llm-interactive-proxy/connector-support/openaicompat => ../../connector-support/openaicompat
