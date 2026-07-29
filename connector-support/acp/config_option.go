package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

func (c *client) setSessionConfigOption(ctx context.Context, sessionID, configID, value string) error {
	id := c.rpcID()
	params := map[string]any{
		"sessionId": sessionID,
		"configId":  configID,
		"value":     value,
	}
	pb, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("acp: session/set_config_option marshal params: %w", err)
	}
	req := rpcRequest{JSONRPC: "2.0", ID: jsonRPCNumericID(id), Method: "session/set_config_option", Params: pb}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("acp: session/set_config_option marshal request: %w", err)
	}
	raw, err := c.t.CallUnary(ctx, body, http.StatusOK)
	if err != nil {
		return fmt.Errorf("acp: session/set_config_option call: %w", err)
	}
	var res rpcResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("acp: session/set_config_option decode: %w", err)
	}
	return rpcErrFromBody("session/set_config_option", res.Error)
}
