package toolcallrepair

import (
	"context"
	"encoding/json"
)

func ValidateArgsAgainstSchema(argsJSON []byte, schema json.RawMessage) error {
	return ValidateArgsAgainstSchemaContext(context.Background(), argsJSON, schema)
}

func ValidateArgsAgainstSchemaContext(ctx context.Context, argsJSON []byte, schema json.RawMessage) error {
	compiled, err := packageSchemaCache().GetOrCompileContext(ctx, schema)
	if err != nil {
		return err
	}
	return compiled.ValidateContext(ctx, argsJSON)
}
