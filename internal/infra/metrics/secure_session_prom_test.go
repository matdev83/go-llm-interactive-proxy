package metrics

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestSecureSessionMetricsSink_denialAndStorage(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	p := RegisterSecureSessionProm(r)
	sink := SecureSessionMetricsSink(p)
	if sink == nil {
		t.Fatal("sink")
	}
	sink.ObserveBeginTurnDenied(string(lipapi.SessionDeniedStorageUnavailable))
	sink.ObserveStorageUnavailable()
	sink.ObserveBeginTurnNew()
	sink.ObserveBeginTurnResume()
	sink.ObserveActivityTouch(0.001)
	sink.ObserveRecorderClientTurnFailed(true)
	sink.ObserveRecorderStreamEventFailed(true, true)

	if got := testutil.ToFloat64(p.denied.WithLabelValues(string(lipapi.SessionDeniedStorageUnavailable))); got != 1 {
		t.Fatalf("denied: %v", got)
	}
	if got := testutil.ToFloat64(p.storageFail); got != 1 {
		t.Fatalf("storage: %v", got)
	}
}

func TestNewBundle_includesSecureSession(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Observability: config.ObservabilityConfig{Metrics: config.MetricsConfig{Enabled: true}}}
	b := NewBundle(cfg)
	if b.SecureSession == nil {
		t.Fatal("expected SecureSession prom")
	}
	if b.SecureSessionMetricsSink() == nil {
		t.Fatal("expected sink")
	}
}
