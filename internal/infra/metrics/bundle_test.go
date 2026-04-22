package metrics

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNewBundle_executorSink(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Observability: config.ObservabilityConfig{
			Metrics: config.MetricsConfig{ExemplarsEnabled: true},
		},
	}
	b := NewBundle(cfg)
	if b == nil || b.Registry == nil || b.HTTP == nil || b.Executor == nil || b.Upstream == nil {
		t.Fatal("expected non-nil bundle components")
	}
	sink := b.ExecutorSink()
	if sink == nil {
		t.Fatal("expected sink")
	}
	sink.OnAttemptRecorded(lipapi.AttemptSuccess, "bedrock")
	sink.OnBackendOpenDuration("bedrock", 0.42)
}
