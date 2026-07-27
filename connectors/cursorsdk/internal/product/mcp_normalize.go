package product

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// NormalizeMCPServers returns deterministic, order-independent JSON for MCP
// server config. Empty/null input yields nil. Non-object values are rejected.
func NormalizeMCPServers(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if !json.Valid(trimmed) {
		return nil, fmt.Errorf("cursorsdk: mcp_servers must be valid JSON")
	}
	var probe any
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&probe); err != nil {
		return nil, fmt.Errorf("cursorsdk: mcp_servers must be valid JSON: %w", err)
	}
	switch probe.(type) {
	case map[string]any:
	default:
		return nil, fmt.Errorf("cursorsdk: mcp_servers must be a JSON object")
	}
	canon, err := json.Marshal(probe)
	if err != nil {
		return nil, fmt.Errorf("cursorsdk: mcp_servers normalize: %w", err)
	}
	if len(canon) > MaxMCPConfigBytes {
		return nil, fmt.Errorf("cursorsdk: mcp_servers exceeds %d byte limit", MaxMCPConfigBytes)
	}
	return canon, nil
}
