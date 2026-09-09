module github.com/matdev83/go-llm-interactive-proxy/connectors/oci

go 1.26.6

replace github.com/matdev83/go-llm-interactive-proxy => ../..

require (
	github.com/Microsoft/go-winio v0.6.2
	github.com/matdev83/go-llm-interactive-proxy v0.0.0-00010101000000-000000000000
	github.com/oracle/oci-go-sdk/v65 v65.124.2
	google.golang.org/grpc v1.83.2
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/gofrs/flock v0.10.0 // indirect
	github.com/sony/gobreaker/v2 v2.4.0 // indirect
	github.com/youmark/pkcs8 v0.0.0-20240726163527-a2c0da244d78 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260819154853-08b0e4226688 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
