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
	allPresent := b != nil &&
		b.Registry != nil &&
		b.HTTP != nil &&
		b.Executor != nil &&
		b.SecureSession != nil &&
		b.ExtensionStages != nil &&
		b.Upstream != nil
	if !allPresent {
		t.Fatal("expected non-nil bundle components")
	}
	if b.ExtensionStageSink() == nil {
		t.Fatal("expected extension stage sink")
	}
	sink := b.ExecutorSink()
	if sink == nil {
		t.Fatal("expected sink")
	}
	sink.OnAttemptRecorded(lipapi.AttemptSuccess, "bedrock")
	sink.OnBackendOpenDuration("bedrock", 0.42)
}
