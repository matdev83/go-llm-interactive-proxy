package acp

import (
	"context"
	"encoding/json"
)

// ServerRequestHandler answers inbound JSON-RPC requests from the agent (e.g. permissions,
// vendor extensions). Stdio transports interleave these with session/update lines; HTTP
// test transports typically do not emit them.
type ServerRequestHandler interface {
	HandleServerRequest(ctx context.Context, method string, id json.RawMessage, params json.RawMessage) (result any, err error)
}

// headlessServerRequestHandler responds with empty objects for unknown methods so a
// headless proxy can keep streaming; vendor-specific connectors replace this.
type headlessServerRequestHandler struct{}

func (headlessServerRequestHandler) HandleServerRequest(_ context.Context, _ string, _ json.RawMessage, _ json.RawMessage) (any, error) {
	return map[string]any{}, nil
}

func serverHandlerOrDefault(h ServerRequestHandler) ServerRequestHandler {
	if h != nil {
		return h
	}
	return headlessServerRequestHandler{}
}

func isInboundServerRequest(probe map[string]any) bool {
	if probe == nil {
		return false
	}
	method, ok := probe["method"].(string)
	if !ok || method == "" {
		return false
	}
	if method == "session/update" {
		return false
	}
	if probe["result"] != nil || probe["error"] != nil {
		return false
	}
	id := probe["id"]
	if id == nil {
		return false
	}
	return true
}

func replyServerRequestJSON(id json.RawMessage, result any) ([]byte, error) {
	res := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}
	return json.Marshal(res)
}
