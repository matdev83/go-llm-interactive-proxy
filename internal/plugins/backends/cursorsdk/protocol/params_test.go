package protocol_test

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/stretchr/testify/require"
)

func TestDecodeMethodParamsTypedShapes(t *testing.T) {
	t.Parallel()

	initP, err := protocol.DecodeMethodParams(protocol.MethodInitialize, json.RawMessage(`{"implVersion":"1.0.0","future":1}`))
	require.NoError(t, err)
	initParams, ok := initP.(protocol.InitializeParams)
	require.True(t, ok)
	require.Equal(t, "1.0.0", initParams.ImplVersion)

	_, err = protocol.DecodeMethodParams(protocol.MethodHealth, json.RawMessage(`{}`))
	require.NoError(t, err)

	models, err := protocol.DecodeMethodParams(protocol.MethodModelsList, json.RawMessage(`{"apiKey":"k","extra":true}`))
	require.NoError(t, err)
	modelParams, ok := models.(protocol.ModelsListParams)
	require.True(t, ok)
	require.Equal(t, "k", modelParams.APIKey)

	createRaw := `{"apiKey":"k","model":{"id":"m1"},"local":{"cwd":"/tmp/ws"},"settingSources":[],"sandboxOptions":{"enabled":false},"autoReview":false,"enableAgentRetries":false,"mcpServers":{"s":{"command":"echo"}},"futureOpt":1}`
	create, err := protocol.DecodeMethodParams(protocol.MethodAgentCreate, json.RawMessage(createRaw))
	require.NoError(t, err)
	cp, ok := create.(protocol.AgentCreateParams)
	require.True(t, ok)
	require.Equal(t, "/tmp/ws", cp.Local.Cwd)
	require.Equal(t, "m1", cp.Model.ID)
	require.False(t, cp.EnableAgentRetries)

	send, err := protocol.DecodeMethodParams(protocol.MethodAgentSend, json.RawMessage(`{"agentId":"a1","prompt":"hi","future":2}`))
	require.NoError(t, err)
	sendParams, ok := send.(protocol.AgentSendParams)
	require.True(t, ok)
	require.Equal(t, "a1", sendParams.AgentID)

	_, err = protocol.DecodeMethodParams(protocol.MethodRunCancel, json.RawMessage(`{"runId":"r1"}`))
	require.NoError(t, err)
	_, err = protocol.DecodeMethodParams(protocol.MethodAgentDispose, json.RawMessage(`{"agentId":"a1"}`))
	require.NoError(t, err)
	_, err = protocol.DecodeMethodParams(protocol.MethodBridgeShutdown, json.RawMessage(`{}`))
	require.NoError(t, err)
}

func TestDecodeMethodParamsRejectsStructuralIssues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		method string
		raw    string
	}{
		{"create_top_level_cwd", protocol.MethodAgentCreate, `{"apiKey":"k","model":{"id":"m"},"cwd":"/tmp","settingSources":[],"sandboxOptions":{"enabled":false},"autoReview":false,"enableAgentRetries":false}`},
		{"create_missing_local", protocol.MethodAgentCreate, `{"apiKey":"k","model":{"id":"m"},"settingSources":[],"sandboxOptions":{"enabled":false},"autoReview":false,"enableAgentRetries":false}`},
		{"send_missing_prompt", protocol.MethodAgentSend, `{"agentId":"a1"}`},
		{"models_missing_key", protocol.MethodModelsList, `{}`},
		{"health_with_api_key", protocol.MethodHealth, `{"apiKey":"secret-key-value"}`},
		{"send_with_api_key", protocol.MethodAgentSend, `{"agentId":"a","prompt":"p","apiKey":"secret-key-value"}`},
		{"cancel_with_api_key", protocol.MethodRunCancel, `{"runId":"r","apiKey":"secret-key-value"}`},
		{"dispose_with_api_key", protocol.MethodAgentDispose, `{"agentId":"a","apiKey":"secret-key-value"}`},
		{"shutdown_with_api_key", protocol.MethodBridgeShutdown, `{"apiKey":"secret-key-value"}`},
		{"initialize_with_api_key", protocol.MethodInitialize, `{"implVersion":"1","apiKey":"secret-key-value"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := protocol.DecodeMethodParams(tc.method, json.RawMessage(tc.raw))
			require.Error(t, err)
			var pe *protocol.ProtocolError
			require.ErrorAs(t, err, &pe)
			require.Equal(t, protocol.ErrorInvalidRequest, pe.Class)
		})
	}
}

func TestSafeErrorBodyDoesNotEchoAPIKey(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"apiKey":"super-secret-key-xyz","agentId":"a"}`)
	secrets := protocol.CollectParamSecrets(raw)
	require.Contains(t, secrets, "super-secret-key-xyz")
	leaky := protocol.SafeErrorBody(protocol.ErrorInvalidRequest, "rejected apiKey=super-secret-key-xyz", secrets...)
	require.NotContains(t, leaky.Message, "super-secret-key-xyz")
	require.Contains(t, leaky.Message, "[REDACTED]")

	_, decodeErr := protocol.DecodeMethodParams(protocol.MethodAgentSend, raw)
	require.Error(t, decodeErr)
	body := protocol.FormatParamError(protocol.MethodAgentSend, raw, decodeErr)
	require.Equal(t, protocol.ErrorInvalidRequest, body.Code)
	require.NotContains(t, body.Message, "super-secret-key-xyz")
}

func TestValidFramesFixtureDecodesTypedParams(t *testing.T) {
	t.Parallel()
	raw := mustReadFixture(t, "protocol/valid_frames.ndjson")
	for _, line := range splitNonEmptyLines(raw) {
		f, err := protocol.DecodeLine(line)
		require.NoError(t, err)
		if f.Type != protocol.TypeRequest {
			continue
		}
		_, err = protocol.DecodeMethodParams(f.Method, f.Params)
		require.NoError(t, err, "method=%s params=%s", f.Method, string(f.Params))
	}
}

func TestInitializeCapabilitiesMatchRequiredMethods(t *testing.T) {
	t.Parallel()
	raw := mustReadFixture(t, "protocol/valid_frames.ndjson")
	lines := splitNonEmptyLines(raw)
	require.GreaterOrEqual(t, len(lines), 2)
	f, err := protocol.DecodeLine(lines[1])
	require.NoError(t, err)
	var result protocol.InitializeResult
	require.NoError(t, json.Unmarshal(f.Result, &result))
	require.Equal(t, protocol.RequiredMethods(), result.Capabilities)
}
