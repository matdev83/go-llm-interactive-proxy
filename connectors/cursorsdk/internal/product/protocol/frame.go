package protocol

import "encoding/json"

// Frame is one NDJSON bridge envelope. Unknown optional fields are ignored by
// standard JSON unmarshaling into this DTO.
type Frame struct {
	SchemaVersion int             `json:"schemaVersion"`
	ImplVersion   string          `json:"implVersion,omitempty"`
	Type          string          `json:"type"`
	ID            string          `json:"id,omitempty"`
	Method        string          `json:"method,omitempty"`
	Params        json.RawMessage `json:"params,omitempty"`
	Result        json.RawMessage `json:"result,omitempty"`
	Error         *ErrorBody      `json:"error,omitempty"`
	RunID         string          `json:"runId,omitempty"`
	Seq           *int64          `json:"seq,omitempty"`
	Kind          string          `json:"kind,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

// ErrorBody is a safe protocol error envelope (no secrets, no raw payloads).
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// InitializeParams is the bridge/initialize request payload.
type InitializeParams struct {
	ImplVersion string `json:"implVersion"`
}

// InitializeResult is the bridge/initialize success payload.
type InitializeResult struct {
	SchemaVersion    int      `json:"schemaVersion"`
	ImplVersion      string   `json:"implVersion"`
	SDKVersion       string   `json:"sdkVersion"`
	NodeVersion      string   `json:"nodeVersion"`
	Capabilities     []string `json:"capabilities"`
	SandboxSupported bool     `json:"sandboxSupported"`
}

// HealthResult is the bridge/health success payload.
type HealthResult struct {
	OK         bool `json:"ok"`
	Generation int  `json:"generation"`
}

// ModelRow is a normalized models/list row (bridge-private, not lipapi).
type ModelRow struct {
	ID          string           `json:"id"`
	DisplayName string           `json:"displayName"`
	Parameters  []ModelParameter `json:"parameters,omitempty"`
	Variants    []ModelVariant   `json:"variants,omitempty"`
}

// ModelParameter describes one SDK model parameter definition.
type ModelParameter struct {
	ID     string   `json:"id"`
	Type   string   `json:"type,omitempty"`
	Values []string `json:"values,omitempty"`
}

// ModelVariant is a bridge-private catalog preset; ID optional (non-routable when empty).
type ModelVariant struct {
	ID          string         `json:"id,omitempty"`
	DisplayName string         `json:"displayName,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
}

// ModelsListResult is the models/list success payload.
type ModelsListResult struct {
	Models []ModelRow `json:"models"`
}

// AgentCreateResult is the agent/create success payload.
type AgentCreateResult struct {
	AgentID string `json:"agentId"`
}

// AgentSendResult is the agent/send success payload.
type AgentSendResult struct {
	RunID string `json:"runId"`
}

// RunCancelResult is the run/cancel success payload.
type RunCancelResult struct {
	Cancelled bool `json:"cancelled"`
}

// AgentDisposeResult is the agent/dispose success payload.
type AgentDisposeResult struct {
	Disposed bool `json:"disposed"`
}

// ShutdownResult is the bridge/shutdown success payload.
type ShutdownResult struct {
	Shutdown bool `json:"shutdown"`
}
