package acp

import (
	"encoding/json"
	"strings"
)

// RPCError is a JSON-RPC error object returned by an ACP agent. Error() stays
// stable for aggregation. Message is operator-facing and may include a concise
// detail extracted from the response's data field.
type RPCError struct {
	Method  string
	Code    int64
	Message string
}

func (e *RPCError) Error() string {
	if e == nil {
		return "acp: json-rpc error"
	}
	if m := e.Method; m != "" {
		return "acp: " + m + ": json-rpc error"
	}
	return "acp: json-rpc error"
}

func rpcErrFromBody(method string, body *rpcErrorBody) error {
	if body == nil {
		return nil
	}
	return &RPCError{
		Method:  method,
		Code:    int64(body.Code),
		Message: formatACPErrorBody(body),
	}
}

func formatACPErrorBody(body *rpcErrorBody) string {
	message := strings.TrimSpace(body.Message)
	if message == "" {
		message = "unknown error"
	}
	if len(body.Data) == 0 || string(body.Data) == "null" {
		return message
	}

	var data any
	if err := json.Unmarshal(body.Data, &data); err != nil {
		return message
	}
	return formatACPError(map[string]any{"message": message, "data": data})
}
