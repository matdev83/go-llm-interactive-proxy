package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	accountingobs "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/observability"
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
	sink.OnTransportNegotiation(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeStreaming, "accept")
	mfs, err := b.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var dump strings.Builder
	for _, mf := range mfs {
		dump.WriteString(mf.String())
	}
	for _, want := range []string{
		"lip_executor_transport_negotiations_total",
		"openai.chat_completions",
		"streaming",
		"accept",
	} {
		if !strings.Contains(dump.String(), want) {
			t.Fatalf("metrics missing %q:\n%s", want, dump.String())
		}
	}
}

func TestTokenAccountingPromRecordsBoundedObservations(t *testing.T) {
	t.Parallel()
	b := NewBundle(&config.Config{})
	sink := b.TokenAccountingObservabilitySink()
	if sink == nil {
		t.Fatal("TokenAccountingObservabilitySink returned nil")
	}
	obs, err := accountingobs.NewObservation(accountingobs.Input{
		Labels: accountingobs.Labels{
			Backend:   "openai",
			Model:     "gpt-4o-mini",
			Plane:     accountingobs.PlaneClientVisible,
			Source:    accountingobs.SourceLocalTokenizer,
			Authority: accountingobs.AuthorityEstimated,
		},
		Status:            accountingobs.StatusUnavailable,
		UnavailableReason: "provider secret raw prompt unsupported",
		Duration:          25 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	sink.Record(obs)
	mfs, err := b.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var dump strings.Builder
	for _, mf := range mfs {
		dump.WriteString(mf.String())
	}
	s := dump.String()
	for _, want := range []string{"lip_token_accounting_observations_total", "lip_token_accounting_observation_seconds", "client_visible", "local_tokenizer", "estimated", "unavailable", "openai", "specified"} {
		if !strings.Contains(s, want) {
			t.Fatalf("metrics missing %q:\n%s", want, s)
		}
	}
	for _, forbidden := range []string{"raw prompt", "secret", "gpt-4o-mini"} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("metrics leaked %q:\n%s", forbidden, s)
		}
	}
}
