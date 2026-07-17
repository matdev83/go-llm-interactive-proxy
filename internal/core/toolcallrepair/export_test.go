package toolcallrepair

import (
	"context"
	"encoding/json"
)

func ExportPreflightSchemaJSON(ctx context.Context, schema []byte, limits SchemaLimits) error {
	return preflightSchemaJSON(ctx, schema, limits)
}

func ExportPreflightArgsJSON(ctx context.Context, args []byte, maxArgsBytes int) error {
	return preflightArgsJSON(ctx, args, maxArgsBytes)
}

func ExportParseOrderedJSON(data []byte) (any, error) {
	return parseOrderedJSON(data)
}

func ExportRepairArgsJSON(ctx context.Context, args []byte, schema json.RawMessage, maxArgsBytes int, schemaLimits SchemaLimits) (out []byte, reason string, err error) {
	return repairArgsJSON(ctx, args, schema, maxArgsBytes, schemaLimits)
}

func ExportMapEngineSchemaReason(err error) string {
	return mapEngineSchemaReason(err)
}

func ExportSchemaShapeLimits(limits SchemaLimits) (maxArrayElems, maxNodes, maxProperties int) {
	l := schemaShapeLimits(limits)
	return l.MaxArrayElems, limits.normalized().MaxNodes, limits.normalized().MaxProperties
}
