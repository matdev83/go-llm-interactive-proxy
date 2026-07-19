package protocol_test

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/stretchr/testify/require"
)

func TestEncodeFrameDoesNotMutateInput(t *testing.T) {
	t.Parallel()
	f := &protocol.Frame{
		Type:   protocol.TypeRequest,
		ID:     "r1",
		Method: protocol.MethodHealth,
		Params: json.RawMessage(`{}`),
	}
	raw, err := protocol.EncodeFrame(f)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"schemaVersion":1`)
	require.Equal(t, 0, f.SchemaVersion)
}

func TestMatchResponseIDAndMethodMismatch(t *testing.T) {
	t.Parallel()
	req := &protocol.Frame{
		SchemaVersion: protocol.SchemaVersion,
		Type:          protocol.TypeRequest,
		ID:            "req-a",
		Method:        protocol.MethodHealth,
		Params:        json.RawMessage(`{}`),
	}
	idMismatch := &protocol.Frame{
		SchemaVersion: protocol.SchemaVersion,
		Type:          protocol.TypeResponse,
		ID:            "req-b",
		Method:        protocol.MethodHealth,
		Result:        json.RawMessage(`{"ok":true}`),
	}
	err := protocol.MatchResponse(req, idMismatch)
	var pe *protocol.ProtocolError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, protocol.ErrorResponseMismatch, pe.Class)
	require.Contains(t, pe.Message, "id mismatch")

	methodMismatch := &protocol.Frame{
		SchemaVersion: protocol.SchemaVersion,
		Type:          protocol.TypeResponse,
		ID:            "req-a",
		Method:        protocol.MethodModelsList,
		Result:        json.RawMessage(`{"models":[]}`),
	}
	err = protocol.MatchResponse(req, methodMismatch)
	require.ErrorAs(t, err, &pe)
	require.Equal(t, protocol.ErrorResponseMismatch, pe.Class)
	require.Contains(t, pe.Message, "method mismatch")
}

func TestResponseResultErrorExclusivity(t *testing.T) {
	t.Parallel()
	_, err := protocol.DecodeLineString(`{"schemaVersion":1,"type":"response","id":"r1","method":"bridge/health","result":{"ok":true},"error":{"code":"x","message":"y"}}`)
	var pe *protocol.ProtocolError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, protocol.ErrorResponseMismatch, pe.Class)
}

func TestNonIntegerSeqRejected(t *testing.T) {
	t.Parallel()
	_, err := protocol.DecodeLineString(`{"schemaVersion":1,"type":"event","runId":"run-1","seq":1.5,"kind":"text_delta","payload":{"text":"x"}}`)
	var pe *protocol.ProtocolError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, protocol.ErrorInvalidEvent, pe.Class)
}
