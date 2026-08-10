# backendplugin/v1 wire contract

Protobuf/gRPC ABI for executable backend connectors.

## Pinned tool versions

Pinned in the repository root `go.mod` via Go 1.26 `tool` directives:

| Tool | Version |
|---|---|
| buf CLI | 1.66.0 |
| protoc-gen-go | v1.36.11 |
| protoc-gen-go-grpc | v1.5.1 |

## Generate

From repository root:

```bash
go install tool
cd api
buf generate --template buf.gen.yaml
```

Confirm generated headers report `protoc-gen-go v1.36.11` and `protoc-gen-go-grpc v1.5.1`. Do not hand-edit `*.pb.go`.

`Invocation.proxy_owned_session_id` is field 20 and is additive. It is usable
only after protocol minor 4 negotiation with the `proxy_owned_session_id`
feature. Older hosts must reject calls that carry proxy-owned session authority;
silently dropping the authority would make native-context partitioning unsafe.

`ExecuteServerFrame.accounting_evidence` is a host-only, additive sideband
introduced at protocol minor 5 and gated by `accounting_evidence_sideband`.
It carries bounded provider billing evidence with explicit counter presence,
source, authority, plane, and dedupe key. It is not a canonical event and must
be consumed by the host exactly once; older peers must disable native compaction
rather than synthesize a native usage lifecycle.
