package toolcallrepair

import (
	"encoding/json"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

// deterministicRootPendingValue resolves only an exact root properties entry.
// The effective schema document is the same bounded, pre-scanned document held
// by the compiled schema, so pending repair and final validation share one schema
// interpretation and one cache compilation.
func deterministicRootPendingValue(_ json.RawMessage, compiled *CompiledSchema, property string) ([]byte, string, bool, error) {
	if compiled == nil || compiled.document == nil {
		return nil, "", false, nil
	}
	effective, err := effectiveSchemaObject(compiled.document, compiled.document, 0)
	if err != nil {
		return nil, "", false, err
	}
	if effective == nil {
		return nil, "", false, nil
	}
	_, props, ok := objectFields(effective["properties"])
	if !ok {
		return nil, "", false, nil
	}
	propertySchema, ok := asSchemaMap(props[property])
	if !ok {
		return nil, "", false, nil
	}
	value, reason, ok, err := deterministicFill(propertySchema, compiled.document, 0)
	if err != nil || !ok {
		return nil, reason, ok, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, "", false, &repairErr{reason: toolcall.ReasonUnrepairable}
	}
	return encoded, reason, true, nil
}
