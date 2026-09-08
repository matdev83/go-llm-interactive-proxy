package keepwarm

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestPrometheusCollectorAllowsAccountingErrors(t *testing.T) {
	t.Parallel()
	if !metricEventAllowed("accounting_error") {
		t.Fatal("accounting_error must be exported as a bounded keep-warm event")
	}
}

func TestPrometheusCollectorExportsBoundedManagerState(t *testing.T) {
	t.Parallel()
	registry := prometheus.NewRegistry()
	prom := NewPrometheusCollector()
	if err := registry.Register(prom); err != nil {
		t.Fatalf("Register: %v", err)
	}
	manager, err := NewManager(DefaultConfig(), ClockFunc(func() time.Time { return time.Unix(100, 0).UTC() }), Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	prom.SetManager(manager)
	observation := promptcache.Observation{
		ALegID: "a", BLegID: "b", BackendInstanceID: "backend", TargetID: "target", GenerationID: "generation",
		Lifecycle: promptcache.LifecycleSlidingExpiry,
		Timing:    promptcache.Timing{ObservedAt: time.Unix(100, 0).UTC(), ExpiresAt: new(time.Unix(400, 0).UTC())},
		Renewable: true, Handle: promptcache.Handle("opaque"),
	}
	result := manager.ArmFromCommittedTurn(ArmInput{
		ALegID: "a", BLegID: "b", BackendInstanceID: "backend", CommittedSuccessful: true,
		ToolEvents:   []lipapi.ToolEvent{{Kind: lipapi.ToolEventFinished, ToolCallID: "tool", ToolName: "bash", Category: lipapi.ToolCategoryOSCommand}},
		Observations: []promptcache.Observation{observation}, Controller: testControllerForMetricsCollector{},
	})
	if !result.Armed {
		t.Fatal(result)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	assertMetricGauge(t, families, "lip_prompt_cache_keepwarm_active_epochs", 1)
	assertMetricGauge(t, families, "lip_prompt_cache_keepwarm_active_targets", 1)
}

type testControllerForMetricsCollector struct{}

func (testControllerForMetricsCollector) Renew(context.Context, promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	return promptcache.RenewResponse{}, nil
}

func (testControllerForMetricsCollector) Release(context.Context, promptcache.ReleaseRequest) error {
	return nil
}

func assertMetricGauge(t *testing.T, families []*dto.MetricFamily, name string, want float64) {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name || len(family.GetMetric()) == 0 {
			continue
		}
		if got := family.GetMetric()[0].GetGauge().GetValue(); got != want {
			t.Fatalf("%s=%v want %v", name, got, want)
		}
		return
	}
	t.Fatalf("metric %s not found", name)
}
