package metrics

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestReloadProm_CardinalityBoundedFixedLabels(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := RegisterReloadProm(reg)
	if m == nil {
		t.Fatal("expected ReloadProm")
	}

	secret := "sk-live-reload-secret-TOKEN-9f3a"
	path := "/etc/lip/config-with-" + secret + ".yaml"
	modelID := "gpt-hostile-" + secret
	backendID := "backend-" + secret

	for i := range 200 {
		trig := "api"
		if i%2 == 0 {
			trig = "sighup"
		}
		// Hostile callers may try to inject unbounded / secret label values.
		m.ObserveAttempt(trig, fmt.Sprintf("published/%s/%s", path, modelID), time.Millisecond)
		m.ObserveStage("compile", fmt.Sprintf("preparation-failed:%s:%s", backendID, secret), time.Millisecond)
		m.ObserveAttempt(path, modelID, time.Millisecond)                       // invalid trigger → other
		m.ObserveStage(secret, "error:"+fmt.Sprint(i)+secret, time.Millisecond) // invalid stage → other
	}
	m.ApplyGenerationSnapshot(ReloadGenerationSnapshot{
		Active:            1,
		Retired:           2,
		Pinned:            4,
		RetentionPressure: 1,
	})

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	var attemptSeries, stageSeries int
	forbiddenLabelNames := map[string]bool{
		"generation_id": true,
		"path":          true,
		"model_id":      true,
		"backend_id":    true,
		"error":         true,
		"secret":        true,
	}
	for _, f := range families {
		switch f.GetName() {
		case "lip_reload_attempts_total":
			attemptSeries = len(f.GetMetric())
		case "lip_reload_stage_duration_seconds":
			stageSeries = len(f.GetMetric())
		case "lip_reload_duration_seconds",
			"lip_reload_active_generations",
			"lip_reload_retired_generations",
			"lip_reload_pinned_references",
			"lip_reload_retention_pressure":
			// present
		default:
			if strings.HasPrefix(f.GetName(), "lip_reload_") {
				t.Fatalf("unexpected reload metric %q", f.GetName())
			}
			continue
		}
		for _, metric := range f.GetMetric() {
			for _, lp := range metric.GetLabel() {
				name := lp.GetName()
				val := lp.GetValue()
				if forbiddenLabelNames[name] {
					t.Fatalf("forbidden label name %q on %s", name, f.GetName())
				}
				if strings.Contains(val, secret) || strings.Contains(val, path) ||
					strings.Contains(val, modelID) || strings.Contains(val, backendID) ||
					strings.Contains(val, "hostile-") || strings.Contains(val, "/etc/") {
					t.Fatalf("%s label %s=%q leaked unbounded/secret material", f.GetName(), name, val)
				}
			}
		}
	}
	// Fixed vocabularies: trigger×result and stage×result only.
	const maxAttemptSeries = 32
	const maxStageSeries = 64
	if attemptSeries == 0 || attemptSeries > maxAttemptSeries {
		t.Fatalf("attempt series=%d want 1..%d", attemptSeries, maxAttemptSeries)
	}
	if stageSeries == 0 || stageSeries > maxStageSeries {
		t.Fatalf("stage series=%d want 1..%d", stageSeries, maxStageSeries)
	}

	gauges := map[string]float64{}
	for _, f := range families {
		switch f.GetName() {
		case "lip_reload_active_generations",
			"lip_reload_retired_generations",
			"lip_reload_pinned_references",
			"lip_reload_retention_pressure":
			gauges[f.GetName()] = reloadGaugeVal(f)
		}
	}
	if gauges["lip_reload_active_generations"] != 1 {
		t.Fatalf("active=%v want 1", gauges["lip_reload_active_generations"])
	}
	if gauges["lip_reload_retired_generations"] != 2 {
		t.Fatalf("retired=%v want 2", gauges["lip_reload_retired_generations"])
	}
	if gauges["lip_reload_pinned_references"] != 4 {
		t.Fatalf("pinned=%v want 4", gauges["lip_reload_pinned_references"])
	}
	if gauges["lip_reload_retention_pressure"] != 1 {
		t.Fatalf("pressure=%v want 1", gauges["lip_reload_retention_pressure"])
	}
}

func TestBundle_WiresReloadProm(t *testing.T) {
	t.Parallel()
	b := NewBundle(nil, nil)
	if b == nil || b.Reload == nil {
		t.Fatal("metrics.Bundle must own ReloadProm")
	}
}

func reloadGaugeVal(f *dto.MetricFamily) float64 {
	if f == nil || len(f.Metric) == 0 || f.Metric[0].Gauge == nil {
		return -1
	}
	return f.Metric[0].Gauge.GetValue()
}
