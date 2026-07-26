package cursorsdk

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func FuzzMapBridgeEvent(f *testing.F) {
	f.Add([]byte(`{"text":"hi"}`), int64(1), "text_delta", "run-1")
	f.Add([]byte(`{"text":"think"}`), int64(1), "reasoning_delta", "run-1")
	f.Add([]byte(`{"inputTokens":1,"outputTokens":2,"totalTokens":3}`), int64(1), "usage", "run-1")
	f.Add([]byte(`{"status":"finished"}`), int64(1), "finished", "run-1")
	f.Add([]byte(`{"message":"warn"}`), int64(1), "warning", "run-1")
	f.Add([]byte(`{}`), int64(1), "activity", "run-1")
	f.Add([]byte(`{`), int64(2), "text_delta", "run-1")
	f.Fuzz(func(t *testing.T, payload []byte, seq int64, kind, runID string) {
		payload = testkit.CapBytes(payload, 64<<10)
		if seq < 0 {
			seq = -seq
		}
		if seq > 1<<20 {
			seq = seq % (1 << 20)
		}
		if runID == "" {
			runID = "run"
		}
		if kind == "" {
			kind = protocol.KindTextDelta
		}
		s := seq
		frame := &protocol.Frame{
			SchemaVersion: protocol.SchemaVersion,
			Type:          protocol.TypeEvent,
			RunID:         runID,
			Seq:           &s,
			Kind:          kind,
			Payload:       json.RawMessage(append([]byte(nil), payload...)),
		}
		_, _ = mapBridgeEvent(frame, runID, seq, "")
		_, _ = mapBridgeEvent(frame, runID, seq+1, "") // sequence mismatch path
		_, _ = mapBridgeEvent(nil, runID, seq, "")
	})
}
