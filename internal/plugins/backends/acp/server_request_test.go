package acp

import (
	"encoding/json"
	"testing"
)

func TestIsInboundServerRequest(t *testing.T) {
	t.Parallel()
	var probe map[string]any
	_ = json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{}}`), &probe)
	if !isInboundServerRequest(probe) {
		t.Fatal("expected server request")
	}
	var probe2 map[string]any
	_ = json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":10,"result":{"stopReason":"end_turn"}}`), &probe2)
	if isInboundServerRequest(probe2) {
		t.Fatal("terminal result is not server request")
	}
	var probe3 map[string]any
	_ = json.Unmarshal([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{}}`), &probe3)
	if isInboundServerRequest(probe3) {
		t.Fatal("session/update notification")
	}
}
