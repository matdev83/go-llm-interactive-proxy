package reftraffictranscript_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftraffictranscript"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

func TestBundleEmit_redactsForObserverNotRaw(t *testing.T) {
	t.Parallel()
	cfg := reftraffictranscript.DefaultConfig()
	pb, obs, raw := reftraffictranscript.BundleForTest(cfg)
	meta := traffic.CaptureMeta{TraceID: "tr", ALegID: "a", BLegID: "b"}
	payload := []byte(`before ` + defaultSecret() + ` after`)
	pb.Emit(context.Background(), traffic.LegCTP, meta, "p", "t", payload)
	if len(raw.Rows) != 1 || !bytes.Equal(raw.Rows[0].Bytes, payload) {
		t.Fatalf("raw %q", raw.Rows[0].Bytes)
	}
	if len(obs.Rows) != 1 {
		t.Fatalf("obs count %d", len(obs.Rows))
	}
	if bytes.Contains(obs.Rows[0].Body, []byte("REF_SECRET")) {
		t.Fatalf("observer should be redacted: %q", obs.Rows[0].Body)
	}
	if !bytes.Contains(obs.Rows[0].Body, []byte("[redacted]")) {
		t.Fatalf("expected placeholder, got %q", obs.Rows[0].Body)
	}
}

func defaultSecret() string { return "REF_SECRET" }

func TestEmit_allFourLegs(t *testing.T) {
	t.Parallel()
	cfg := reftraffictranscript.DefaultConfig()
	pb, obs, _ := reftraffictranscript.BundleForTest(cfg)
	meta := traffic.CaptureMeta{TraceID: "t1"}
	legs := []traffic.Leg{traffic.LegCTP, traffic.LegPTB, traffic.LegBTP, traffic.LegPTC}
	for _, leg := range legs {
		pb.Emit(context.Background(), leg, meta, "p", "t", []byte{byte(leg[0])})
	}
	if len(obs.Rows) != 4 {
		t.Fatalf("got %d rows", len(obs.Rows))
	}
}
