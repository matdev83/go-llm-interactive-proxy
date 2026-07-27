package protocol_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
)

func FuzzDecodeLine(f *testing.F) {
	f.Add([]byte(`{"schemaVersion":1,"type":"request","id":"1","method":"bridge/health","params":{}}`))
	f.Add([]byte(`{"schemaVersion":1,"type":"event","runId":"run-1","seq":1,"kind":"text_delta","payload":{"text":"x"}}`))
	f.Add([]byte(`{"schemaVersion":1,"type":"response","id":"1","method":"bridge/health","result":{"ok":true}}`))
	f.Add([]byte(`not-json`))
	f.Add([]byte(`{"schemaVersion":1,"type":"event","runId":"r","seq":2,"kind":"text_delta","payload":{}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = capFuzzBytes(raw, protocol.MaxFrameBytes+1024)
		frame, err := protocol.DecodeLine(raw)
		if err != nil || frame == nil {
			return
		}
		seq := protocol.NewRunSequencer()
		if frame.Type == protocol.TypeEvent {
			_ = seq.Accept(frame)
		}
		_, _ = protocol.EncodeFrame(frame)
	})
}

func capFuzzBytes(b []byte, max int) []byte {
	if max <= 0 || len(b) <= max {
		return b
	}
	return b[:max]
}
