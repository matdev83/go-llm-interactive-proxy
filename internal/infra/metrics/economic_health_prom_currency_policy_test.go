package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Phase 16 review Finding 8 RED contract: the native-currency dimension of
// economic_discrepancy_gross_absolute must be a fixed, reviewable finite
// policy. The pre-fix bucket function accepted every 3–8 uppercase string, so
// a snapshot with N valid-looking codes produced N currency series and
// collapsed unrelated monetary units into currency="other". These tests pin
// the finite vocabulary, the hard series cap, truthful label-free overflow
// accounting and cross-snapshot stability.

const currencyPolicyTestSeriesCap = 64

// hostileCurrencySecret is a stand-in for any credential/identity that must
// never become a metric label or be echoed back.
const hostileCurrencySecret = "SECRET_CURRENCY_TOKEN_xyz"

// syntheticCurrencyCode returns the i-th distinct three-uppercase-letter code,
// a valid-looking ISO-4217-shaped string that used to be accepted verbatim.
func syntheticCurrencyCode(i int) string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	return string([]byte{
		letters[(i/676)%26],
		letters[(i/26)%26],
		letters[i%26],
	})
}

func currencySnapshot(codes []string, amount string) billing.EconomicHealthSnapshot {
	s := economicHealthTestSnapshot()
	gross := make([]billing.EconomicCurrencyAmount, 0, len(codes))
	for _, c := range codes {
		gross = append(gross, billing.EconomicCurrencyAmount{Currency: c, Amount: amount})
	}
	s.Discrepancies.Gross = gross
	return s
}

func grossSeries(t *testing.T, reg *prometheus.Registry) map[string]float64 {
	t.Helper()
	return gatheredEconomicsSeries(t, reg)["lip_economic_discrepancy_gross_absolute"]
}

func TestEconomicHealthPromCurrencySeriesBounded(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := RegisterEconomicHealthProm(reg)

	codes := make([]string, 0, 1024+3)
	for i := 0; i < 1024; i++ {
		codes = append(codes, syntheticCurrencyCode(i))
	}
	// Hostile malformed codes that the old bucket function folded into a
	// shared currency="other" monetary total.
	codes = append(codes, "usd", "U$D", "USD-"+hostileCurrencySecret)
	m.ApplySnapshot(currencySnapshot(codes, "1"))

	gross := grossSeries(t, reg)
	if _, ok := gross["currency=other"]; ok {
		t.Fatalf("currency=other monetary series must not exist: %v", gross)
	}
	if len(gross) > currencyPolicyTestSeriesCap {
		t.Fatalf("gross currency series=%d exceeds hard cap %d", len(gross), currencyPolicyTestSeriesCap)
	}

	series := gatheredEconomicsSeries(t, reg)
	omitted := series["lip_economic_discrepancy_currency_omitted"][""]
	distinct := map[string]struct{}{}
	for _, c := range codes {
		distinct[strings.TrimSpace(c)] = struct{}{}
	}
	if want := float64(len(distinct) - len(gross)); omitted != want {
		t.Fatalf("omitted distinct currencies=%v want %v (distinct=%d emitted=%d)", omitted, want, len(distinct), len(gross))
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var allSeries int
	for _, family := range families {
		if !strings.HasPrefix(family.GetName(), "lip_economic_") {
			continue
		}
		allSeries += len(family.GetMetric())
		for _, metric := range family.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if strings.Contains(lp.GetValue(), hostileCurrencySecret) {
					t.Fatalf("secret leaked into %s label %s=%q", family.GetName(), lp.GetName(), lp.GetValue())
				}
			}
		}
	}
	if allSeries > 128 {
		t.Fatalf("economics series=%d exceeds bounded cap 128", allSeries)
	}
	t.Logf("currency policy: input distinct=%d emitted gross series=%d omitted distinct=%v total lip_economic_ series=%d",
		len(distinct), len(gross), omitted, allSeries)
}

func TestEconomicHealthPromSupportedCurrenciesExactWithTruthfulOmission(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := RegisterEconomicHealthProm(reg)

	s := economicHealthTestSnapshot()
	s.Discrepancies.Gross = []billing.EconomicCurrencyAmount{
		{Currency: "USD", Amount: "8/25"},
		{Currency: "EUR", Amount: "1/2"},
		{Currency: "usd", Amount: "1000"},
		{Currency: "U$D", Amount: "1000"},
		{Currency: "OTHER", Amount: "1000"},
	}
	m.ApplySnapshot(s)

	series := gatheredEconomicsSeries(t, reg)
	gross := series["lip_economic_discrepancy_gross_absolute"]
	if gross["currency=USD"] != 0.32 {
		t.Fatalf("USD gross=%v want 0.32", gross["currency=USD"])
	}
	if gross["currency=EUR"] != 0.5 {
		t.Fatalf("EUR gross=%v want 0.5", gross["currency=EUR"])
	}
	if _, ok := gross["currency=other"]; ok {
		t.Fatalf("mixed-unit other series present: %v", gross)
	}
	if omitted := series["lip_economic_discrepancy_currency_omitted"][""]; omitted != 3 {
		t.Fatalf("omitted=%v want 3 (usd, U$D, OTHER)", omitted)
	}
}

func TestEconomicHealthPromCurrencyVocabularyNeverGrowsAcrossSnapshots(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := RegisterEconomicHealthProm(reg)

	seen := map[string]struct{}{}
	for round := 0; round < 8; round++ {
		codes := make([]string, 0, 130)
		for i := 0; i < 128; i++ {
			codes = append(codes, syntheticCurrencyCode(round*128+i))
		}
		codes = append(codes, "USD", "EUR")
		m.ApplySnapshot(currencySnapshot(codes, "1"))

		gross := grossSeries(t, reg)
		if _, ok := gross["currency=other"]; ok {
			t.Fatalf("round %d produced currency=other", round)
		}
		if len(gross) > currencyPolicyTestSeriesCap {
			t.Fatalf("round %d gross series=%d exceeds cap %d", round, len(gross), currencyPolicyTestSeriesCap)
		}
		for key := range gross {
			seen[strings.TrimPrefix(key, "currency=")] = struct{}{}
		}
	}
	if _, ok := seen["USD"]; !ok {
		t.Fatal("supported USD must stay visible across snapshots")
	}
	if len(seen) > currencyPolicyTestSeriesCap {
		t.Fatalf("observed currency vocabulary=%d exceeds cap %d", len(seen), currencyPolicyTestSeriesCap)
	}
}
