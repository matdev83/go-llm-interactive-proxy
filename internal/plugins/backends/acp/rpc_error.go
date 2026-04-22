package acp

// RPCError is a JSON-RPC error object returned by an ACP agent. Error() is stable for
// aggregation; use Code and Message for operator-facing detail (logs, not Error() strings).
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
		Message: body.Message,
	}
}
