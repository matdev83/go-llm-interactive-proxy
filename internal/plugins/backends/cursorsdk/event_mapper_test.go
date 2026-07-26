package cursorsdk

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapBridgeEvent_TextReasoningUsageAndTerminal(t *testing.T) {
	t.Parallel()
	runID := "run-1"
	seq := int64(1)

	text, seq := mapBridgeEvent(eventFrame(runID, seq, protocol.KindTextDelta, `{"text":"hi"}`), runID, seq, "")
	require.NoError(t, text.err)
	require.Len(t, text.events, 1)
	assert.Equal(t, lipapi.EventTextDelta, text.events[0].Kind)
	assert.Equal(t, "hi", text.events[0].Delta)

	reason, seq := mapBridgeEvent(eventFrame(runID, seq, protocol.KindReasoningDelta, `{"text":"think"}`), runID, seq, "")
	require.NoError(t, reason.err)
	require.Len(t, reason.events, 1)
	assert.Equal(t, lipapi.EventReasoningDelta, reason.events[0].Kind)

	usage, seq := mapBridgeEvent(eventFrame(runID, seq, protocol.KindUsage, `{"inputTokens":1,"outputTokens":2,"totalTokens":3}`), runID, seq, "")
	require.NoError(t, usage.err)
	require.Len(t, usage.events, 1)
	assert.Equal(t, lipapi.EventUsageDelta, usage.events[0].Kind)
	assert.Equal(t, 1, usage.events[0].InputTokens)
	assert.Equal(t, 2, usage.events[0].OutputTokens)
	assert.Equal(t, 3, usage.events[0].TotalTokens)

	fin, seq := mapBridgeEvent(eventFrame(runID, seq, protocol.KindFinished, `{"status":"finished"}`), runID, seq, "")
	require.NoError(t, fin.err)
	require.True(t, fin.terminal)
	require.True(t, fin.success)
	require.Len(t, fin.events, 1)
	assert.Equal(t, lipapi.EventResponseFinished, fin.events[0].Kind)
	_ = seq
}

func TestMapBridgeEvent_DropsActivityNeverToolCall(t *testing.T) {
	t.Parallel()
	runID := "run-act"
	payload := `{"type":"tool_call","name":"Shell","args":{"cmd":"cat /secret"},"content":"SECRET_VALUE"}`
	res, next := mapBridgeEvent(eventFrame(runID, 1, protocol.KindActivity, payload), runID, 1, "")
	require.NoError(t, res.err)
	assert.Empty(t, res.events)
	assert.Equal(t, int64(2), next)
	for _, ev := range res.events {
		assert.NotEqual(t, lipapi.EventToolCallStarted, ev.Kind)
		assert.NotContains(t, ev.WarningMessage, "SECRET")
		assert.NotContains(t, ev.Delta, "SECRET")
	}
}

func TestMapBridgeEvent_WarningSanitizedNoActivityLeak(t *testing.T) {
	t.Parallel()
	runID := "run-warn"
	msg := strings.Repeat("w", lipapi.MaxEventDiagMessageBytes+32)
	res, _ := mapBridgeEvent(eventFrame(runID, 1, protocol.KindWarning, `{"message":`+jsonQuote(msg)+`}`), runID, 1, "")
	require.NoError(t, res.err)
	require.Len(t, res.events, 1)
	assert.Equal(t, lipapi.EventWarning, res.events[0].Kind)
	assert.LessOrEqual(t, len(res.events[0].WarningMessage), MaxStderrRetainBytes)
}

func TestSanitizeWarningMessage_redactsPathsSecretsPromptAndToolContent(t *testing.T) {
	t.Parallel()
	key := "secret-api-key-value"
	in := `fail at /home/user/secret and C:\Users\x\a.go tool_call Shell args:{"cmd":"cat /tmp/x"} content:"LEAK" prompt: hello ` + key
	out := sanitizeWarningMessage(in, key)
	assert.NotContains(t, out, key)
	assert.NotContains(t, out, "/home/user")
	assert.NotContains(t, out, `C:\Users`)
	assert.NotContains(t, out, "hello")
	assert.NotContains(t, out, "LEAK")
	assert.NotContains(t, out, "cat ")
	assert.Contains(t, out, "[PATH]")
	assert.Contains(t, out, "[REDACTED]")
}

func TestMapBridgeEvent_UsageOmitsIncompleteOrNegative(t *testing.T) {
	t.Parallel()
	runID := "run-u"
	partial, next := mapBridgeEvent(eventFrame(runID, 1, protocol.KindUsage, `{"inputTokens":1}`), runID, 1, "")
	require.NoError(t, partial.err)
	assert.Empty(t, partial.events)
	assert.Equal(t, int64(2), next)

	neg, _ := mapBridgeEvent(eventFrame(runID, next, protocol.KindUsage, `{"inputTokens":-1,"outputTokens":1,"totalTokens":1}`), runID, next, "")
	require.NoError(t, neg.err)
	assert.Empty(t, neg.events)
}

func TestMapBridgeEvent_SequenceRegressionAndUnknownKind(t *testing.T) {
	t.Parallel()
	runID := "run-seq"
	bad, kept := mapBridgeEvent(eventFrame(runID, 2, protocol.KindTextDelta, `{"text":"x"}`), runID, 1, "")
	require.Error(t, bad.err)
	assert.Contains(t, bad.err.Error(), protocol.ErrorSequenceRegression)
	assert.Equal(t, int64(1), kept)

	unk, kept := mapBridgeEvent(eventFrame(runID, 1, "tool_call", `{}`), runID, 1, "")
	require.Error(t, unk.err)
	assert.Contains(t, unk.err.Error(), protocol.ErrorUnknownEventKind)
	assert.Equal(t, int64(1), kept)
}

func TestMapBridgeEvent_ErrorTerminalAndCancelledFinished(t *testing.T) {
	t.Parallel()
	runID := "run-e"
	errRes, next := mapBridgeEvent(eventFrame(runID, 1, protocol.KindError, `{"code":"cursor_sdk_run_failed","message":"boom"}`), runID, 1, "")
	require.NoError(t, errRes.err)
	require.True(t, errRes.terminal)
	require.False(t, errRes.success)
	require.Equal(t, lipapi.EventError, errRes.events[0].Kind)

	fin, _ := mapBridgeEvent(eventFrame(runID, next, protocol.KindFinished, `{"status":"cancelled"}`), runID, next, "")
	require.NoError(t, fin.err)
	require.True(t, fin.terminal)
	require.False(t, fin.success)
	assert.Equal(t, "cancelled", fin.events[0].FinishReason)
}

func eventFrame(runID string, seq int64, kind, payload string) *protocol.Frame {
	s := seq
	return &protocol.Frame{
		SchemaVersion: protocol.SchemaVersion,
		Type:          protocol.TypeEvent,
		RunID:         runID,
		Seq:           &s,
		Kind:          kind,
		Payload:       json.RawMessage(payload),
	}
}

func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
