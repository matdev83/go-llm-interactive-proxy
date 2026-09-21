package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Task 16.3B RED contract: bounded economics health series. Metric names
// and label values come from explicit allowlists; hostile queue, reason,
// currency and identity strings collapse to fixed buckets and raw
// secrets/IDs never appear as labels. Series count stays capped.

func economicHealthTestSnapshot() billing.EconomicHealthSnapshot {
	return billing.EconomicHealthSnapshot{
		Queues: []billing.EconomicQueueHealth{
			{Queue: "customer", Pending: 3, Processing: 1, Failed: 1, OldestAgeSec: 90, MaxAttempts: 5,
				Retries: []billing.EconomicRetryCount{{Reason: "transient_failure", Count: 2}}},
			{Queue: "provider", Pending: 2, OldestAgeSec: 30, MaxAttempts: 1},
		},
		Statements: billing.EconomicStatementHealth{Matched: 4, Unmatched: 1},
		Discrepancies: billing.EconomicDiscrepancyHealth{
			ByQuantity: []billing.EconomicStatusCount{{Status: "discrepant", Count: 2}},
			ByMonetary: []billing.EconomicStatusCount{{Status: "complete", Count: 1}},
			Gross:      []billing.EconomicCurrencyAmount{{Currency: "USD", Amount: "8/25"}},
			Partial:    1, Incomparable: 1,
		},
		WindowRows: 3,
		TakenAt:    time.Unix(1_700_050_000, 0).UTC(),
	}
}

func gatheredEconomicsSeries(t *testing.T, reg *prometheus.Registry) map[string]map[string]float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]float64{}
	for _, family := range families {
		name := family.GetName()
		if !strings.HasPrefix(name, "lip_economic_") {
			continue
		}
		labels := map[string]float64{}
		for _, metric := range family.GetMetric() {
			var parts []string
			for _, lp := range metric.GetLabel() {
				parts = append(parts, lp.GetName()+"="+lp.GetValue())
			}
			key := strings.Join(parts, ",")
			if metric.Gauge != nil {
				labels[key] = metric.Gauge.GetValue()
			}
		}
		got[name] = labels
	}
	return got
}

func TestEconomicHealthPromAppliesSnapshot(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := RegisterEconomicHealthProm(reg)
	if m == nil {
		t.Fatal("expected EconomicHealthProm")
	}
	m.ApplySnapshot(economicHealthTestSnapshot())
	series := gatheredEconomicsSeries(t, reg)
	pending, ok := series["lip_economic_queue_pending"]
	if !ok {
		t.Fatal("missing lip_economic_queue_pending series")
	}
	if pending["queue=customer"] != 3 {
		t.Fatalf("customer pending = %v, want 3", pending)
	}
	age, ok := series["lip_economic_queue_oldest_age_seconds"]
	if !ok {
		t.Fatal("missing oldest age series")
	}
	if age["queue=customer"] != 90 {
		t.Fatalf("customer oldest age = %v, want 90", age)
	}
}

func TestEconomicHealthPromBoundsHostileLabels(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := RegisterEconomicHealthProm(reg)
	secret := "SECRET_PROVIDER_TOKEN_xyz"
	snapshot := economicHealthTestSnapshot()
	snapshot.Queues = append(snapshot.Queues, billing.EconomicQueueHealth{
		Queue: "hostile-bc_1-" + secret, Pending: 1,
		Retries: []billing.EconomicRetryCount{{Reason: "weird " + secret, Count: 1}},
	})
	snapshot.Discrepancies.Gross = append(snapshot.Discrepancies.Gross,
		billing.EconomicCurrencyAmount{Currency: "USD-" + secret, Amount: "1"})
	m.ApplySnapshot(snapshot)

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var series int
	for _, family := range families {
		if !strings.HasPrefix(family.GetName(), "lip_economic_") {
			continue
		}
		series += len(family.GetMetric())
		for _, metric := range family.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if strings.Contains(lp.GetValue(), secret) || strings.Contains(lp.GetValue(), "hostile-") || strings.Contains(lp.GetValue(), "bc_1") {
					t.Fatalf("raw identity leaked into %s label %s=%q", family.GetName(), lp.GetName(), lp.GetValue())
				}
			}
		}
	}
	const maxSeries = 128
	if series > maxSeries {
		t.Fatalf("economics series=%d exceeds bounded cap %d", series, maxSeries)
	}
	if series == 0 {
		t.Fatal("expected economics series")
	}
}

func TestEconomicHealthPromMetricNamesAllowlisted(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := RegisterEconomicHealthProm(reg)
	m.ApplySnapshot(economicHealthTestSnapshot())
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"lip_economic_queue_pending": true, "lip_economic_queue_processing": true,
		"lip_economic_queue_completed": true, "lip_economic_queue_processed": true,
		"lip_economic_queue_failed": true, "lip_economic_queue_oldest_age_seconds": true,
		"lip_economic_queue_max_attempts": true, "lip_economic_queue_retries": true,
		"lip_economic_statements": true, "lip_economic_discrepancy_count": true,
		"lip_economic_discrepancy_gross_absolute": true, "lip_economic_discrepancy_window_rows": true,
		"lip_economic_discrepancy_currency_omitted": true,
	}
	for _, family := range families {
		name := family.GetName()
		if !strings.HasPrefix(name, "lip_economic_") {
			continue
		}
		if !allowed[name] {
			t.Fatalf("metric %q is not in the explicit allowlist", name)
		}
	}
	for name := range allowed {
		found := false
		for _, family := range families {
			if family.GetName() == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("allowlisted metric %q was not registered", name)
		}
	}
}

func TestEconomicHealthBundleWiresProm(t *testing.T) {
	t.Parallel()
	b := NewBundle(nil, nil)
	if b == nil || b.EconomicHealth == nil {
		t.Fatal("metrics.Bundle must own EconomicHealthProm")
	}
}
