package metrics_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/prometheus/client_golang/prometheus"
)

func TestPhase6_CancellationsProm_BoundedLabelCardinality_And_SecretSafety(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := metrics.RegisterExecutorProm(reg)
	if m == nil {
		t.Fatal("expected ExecutorProm")
	}

	secretToken := "sk-ant-api03-SECRET_KEY_xyz987654321"
	hostilePayload := "DROP TABLE users; {\"prompt\":\"secret password\"}"

	// Inject 500 distinct hostile / random causes and details
	for i := range 500 {
		rawCause := lipapi.CancelCause{
			Kind:   lipapi.CancelKind(fmt.Sprintf("hostile-kind-%d-%s", i, secretToken)),
			Detail: fmt.Sprintf("hostile-detail-%d-%s-%s", i, secretToken, hostilePayload),
		}
		rawMode := lipapi.CancelMode(fmt.Sprintf("hostile-mode-%d", i))
		rawPhase := fmt.Sprintf("hostile-phase-%d", i)
		rawFallback := fmt.Sprintf("hostile-fallback-%d-%s", i, secretToken)

		m.ObserveCancellation(rawCause, rawMode, rawPhase, rawFallback)
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	var seriesCount int
	for _, f := range families {
		if f.GetName() != "lip_executor_cancellations_total" {
			continue
		}
		found = true
		seriesCount = len(f.GetMetric())
		for _, metric := range f.GetMetric() {
			for _, lp := range metric.GetLabel() {
				val := lp.GetValue()
				if strings.Contains(val, secretToken) {
					t.Fatalf("secret token leaked into label %q: %q", lp.GetName(), val)
				}
				if strings.Contains(val, "hostile") {
					t.Fatalf("unbounded/hostile string leaked into label %q: %q", lp.GetName(), val)
				}
				if strings.Contains(val, "secret password") {
					t.Fatalf("payload leaked into label %q: %q", lp.GetName(), val)
				}
			}
		}
	}

	if !found {
		t.Fatal("expected lip_executor_cancellations_total metric family to be gathered")
	}

	// Hard bound on maximum possible combinations:
	// cause_class(5) * mode(5) * phase(5) * fallback(4) = 500 theoretical max,
	// with bucketed mapping it should be well below 64.
	const maxSeries = 64
	if seriesCount > maxSeries {
		t.Fatalf("cancellations series count %d exceeds bounded limit %d", seriesCount, maxSeries)
	}
	if seriesCount == 0 {
		t.Fatal("expected non-zero series count")
	}
}

func TestPhase6_CancellationsProm_StandardClasses(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := metrics.RegisterExecutorProm(reg)
	if m == nil {
		t.Fatal("expected ExecutorProm")
	}

	m.ObserveCancellation(
		lipapi.CancelCause{Kind: lipapi.CancelExplicit, Detail: "user cancelled"},
		lipapi.CancelModeProvider,
		"outcome",
		"negotiated",
	)

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, f := range families {
		if f.GetName() != "lip_executor_cancellations_total" {
			continue
		}
		for _, metric := range f.GetMetric() {
			labels := map[string]string{}
			for _, lp := range metric.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["cause_class"] == "explicit" &&
				labels["mode"] == "provider" &&
				labels["phase"] == "outcome" &&
				labels["fallback"] == "negotiated" {
				found = true
				if metric.GetCounter().GetValue() != 1 {
					t.Fatalf("expected counter value 1, got %v", metric.GetCounter().GetValue())
				}
			}
		}
	}
	if !found {
		t.Fatal("expected metric with explicit/provider/outcome/negotiated labels")
	}
}
