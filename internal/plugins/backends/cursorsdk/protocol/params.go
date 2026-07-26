package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ModelSelection is a bridge-private model id (+ optional params).
type ModelSelection struct {
	ID     string          `json:"id"`
	Params json.RawMessage `json:"params,omitempty"`
}

// SandboxOptions is the bridge-private sandbox toggle.
type SandboxOptions struct {
	Enabled bool `json:"enabled"`
}

// AgentCreateLocal holds local-runtime create fields (cwd only at this layer).
type AgentCreateLocal struct {
	Cwd string `json:"cwd"`
}

// ModelsListParams is the models/list request payload (apiKey allowed).
type ModelsListParams struct {
	APIKey string `json:"apiKey"`
}

// AgentCreateParams is the agent/create request payload (apiKey allowed).
// Unknown optional JSON fields are ignored by standard decoding.
type AgentCreateParams struct {
	APIKey             string           `json:"apiKey"`
	Model              ModelSelection   `json:"model"`
	Local              AgentCreateLocal `json:"local"`
	SettingSources     []string         `json:"settingSources"`
	SandboxOptions     *SandboxOptions  `json:"sandboxOptions"`
	AutoReview         bool             `json:"autoReview"`
	EnableAgentRetries bool             `json:"enableAgentRetries"`
	MCPServers         json.RawMessage  `json:"mcpServers,omitempty"`
}

// AgentSendParams is the agent/send request payload (starts a run).
type AgentSendParams struct {
	AgentID string `json:"agentId"`
	Prompt  string `json:"prompt"`
}

// RunCancelParams is the run/cancel request payload.
type RunCancelParams struct {
	RunID string `json:"runId"`
}

// AgentDisposeParams is the agent/dispose request payload.
type AgentDisposeParams struct {
	AgentID string `json:"agentId"`
}

// HealthParams is the bridge/health request payload (empty object).
type HealthParams struct{}

// ShutdownParams is the bridge/shutdown request payload (empty object).
type ShutdownParams struct{}

// DecodeMethodParams validates and decodes typed params for a required method.
// Unknown optional fields are ignored. Structural mandatories are enforced.
// apiKey is allowed only for models/list and agent/create.
func DecodeMethodParams(method string, raw json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if !json.Valid(raw) {
		return nil, protoErr(ErrorInvalidRequest, "params must be a JSON object")
	}
	var obj map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return nil, protoErr(ErrorInvalidRequest, "params must be a JSON object")
	}
	if obj == nil {
		return nil, protoErr(ErrorInvalidRequest, "params must be a JSON object")
	}

	switch method {
	case MethodInitialize:
		if err := rejectAPIKey(obj); err != nil {
			return nil, err
		}
		var p InitializeParams
		if err := decodeStrictObject(raw, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.ImplVersion) == "" {
			return nil, protoErr(ErrorInvalidRequest, "missing implVersion")
		}
		return p, nil
	case MethodHealth:
		if err := rejectAPIKey(obj); err != nil {
			return nil, err
		}
		var p HealthParams
		if err := decodeStrictObject(raw, &p); err != nil {
			return nil, err
		}
		return p, nil
	case MethodModelsList:
		var p ModelsListParams
		if err := decodeStrictObject(raw, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.APIKey) == "" {
			return nil, protoErr(ErrorInvalidRequest, "missing apiKey")
		}
		return p, nil
	case MethodAgentCreate:
		for _, key := range []string{"apiKey", "model", "local", "settingSources", "sandboxOptions", "autoReview", "enableAgentRetries"} {
			if _, ok := obj[key]; !ok {
				return nil, protoErr(ErrorInvalidRequest, "missing "+key)
			}
		}
		var p AgentCreateParams
		if err := decodeStrictObject(raw, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.APIKey) == "" {
			return nil, protoErr(ErrorInvalidRequest, "missing apiKey")
		}
		if strings.TrimSpace(p.Model.ID) == "" {
			return nil, protoErr(ErrorInvalidRequest, "missing model.id")
		}
		if strings.TrimSpace(p.Local.Cwd) == "" {
			return nil, protoErr(ErrorInvalidRequest, "missing local.cwd")
		}
		if p.SettingSources == nil {
			return nil, protoErr(ErrorInvalidRequest, "missing settingSources")
		}
		if p.SandboxOptions == nil {
			return nil, protoErr(ErrorInvalidRequest, "missing sandboxOptions")
		}
		if len(p.MCPServers) > 0 && !json.Valid(p.MCPServers) {
			return nil, protoErr(ErrorInvalidRequest, "invalid mcpServers")
		}
		return p, nil
	case MethodAgentSend:
		if err := rejectAPIKey(obj); err != nil {
			return nil, err
		}
		var p AgentSendParams
		if err := decodeStrictObject(raw, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.AgentID) == "" {
			return nil, protoErr(ErrorInvalidRequest, "missing agentId")
		}
		if strings.TrimSpace(p.Prompt) == "" {
			return nil, protoErr(ErrorInvalidRequest, "missing prompt")
		}
		return p, nil
	case MethodRunCancel:
		if err := rejectAPIKey(obj); err != nil {
			return nil, err
		}
		var p RunCancelParams
		if err := decodeStrictObject(raw, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.RunID) == "" {
			return nil, protoErr(ErrorInvalidRequest, "missing runId")
		}
		return p, nil
	case MethodAgentDispose:
		if err := rejectAPIKey(obj); err != nil {
			return nil, err
		}
		var p AgentDisposeParams
		if err := decodeStrictObject(raw, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.AgentID) == "" {
			return nil, protoErr(ErrorInvalidRequest, "missing agentId")
		}
		return p, nil
	case MethodBridgeShutdown:
		if err := rejectAPIKey(obj); err != nil {
			return nil, err
		}
		var p ShutdownParams
		if err := decodeStrictObject(raw, &p); err != nil {
			return nil, err
		}
		return p, nil
	default:
		return nil, protoErr(ErrorUnknownMethod, method)
	}
}

func rejectAPIKey(obj map[string]json.RawMessage) error {
	if _, ok := obj["apiKey"]; ok {
		return protoErr(ErrorInvalidRequest, "apiKey not allowed for this method")
	}
	return nil
}

func decodeStrictObject(raw json.RawMessage, dest any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(dest); err != nil {
		return protoErr(ErrorInvalidRequest, err.Error())
	}
	return nil
}

// RedactSecrets removes known secret substrings from diagnostic text.
func RedactSecrets(message string, secrets ...string) string {
	out := message
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		out = strings.ReplaceAll(out, secret, "[REDACTED]")
	}
	return out
}

// SafeErrorBody builds a protocol error envelope that never echoes secrets.
func SafeErrorBody(code, message string, secrets ...string) ErrorBody {
	return ErrorBody{
		Code:    code,
		Message: RedactSecrets(message, secrets...),
	}
}

// CollectParamSecrets extracts values that must never appear in error text.
func CollectParamSecrets(raw json.RawMessage) []string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	var secrets []string
	if v, ok := obj["apiKey"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil && s != "" {
			secrets = append(secrets, s)
		}
	}
	return secrets
}

// FormatParamError returns a safe invalid_request message for params.
func FormatParamError(method string, raw json.RawMessage, cause error) ErrorBody {
	secrets := CollectParamSecrets(raw)
	msg := fmt.Sprintf("%s params invalid", method)
	if cause != nil {
		msg = msg + ": " + cause.Error()
	}
	return SafeErrorBody(ErrorInvalidRequest, msg, secrets...)
}
