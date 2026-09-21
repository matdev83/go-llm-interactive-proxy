package metrics

import (
	"math/big"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/prometheus/client_golang/prometheus"
)

// Task 16.3B / Phase 16 Finding 8 bounded economics health series. The
// metric-name surface is a fixed allowlist; hostile queue names, retry
// reasons, statuses and identities collapse to fixed buckets and never become
// a label. The native-currency dimension is stricter: it is restricted to a
// static, reviewable finite allowlist (economicHealthSupportedCurrencies)
// because distinct monetary units must never be merged into a shared
// currency="other" total. Unsupported or overflow currencies are dropped from
// economic_discrepancy_gross_absolute and surfaced only through the
// label-free lip_economic_discrepancy_currency_omitted count, so series
// cardinality is mechanically finite across successive snapshots.
type EconomicHealthProm struct {
	queuePending    *prometheus.GaugeVec
	queueProcessing *prometheus.GaugeVec
	queueCompleted  *prometheus.GaugeVec
	queueProcessed  *prometheus.GaugeVec
	queueFailed     *prometheus.GaugeVec
	queueOldestAge  *prometheus.GaugeVec
	queueMaxAttempt *prometheus.GaugeVec
	queueRetries    *prometheus.GaugeVec

	statements         *prometheus.GaugeVec
	discrepancyCount   *prometheus.GaugeVec
	discrepancyGross   *prometheus.GaugeVec
	discrepancyWindows prometheus.Gauge
	// discrepancyCurrencyOmitted is label-free: it counts distinct native
	// currencies dropped from discrepancyGross because they are outside the
	// supported allowlist or beyond the hard series cap. It never carries a
	// raw unsupported code, so overflow cannot grow metric vocabulary.
	discrepancyCurrencyOmitted prometheus.Gauge
}

// RegisterEconomicHealthProm registers the lip_economic_* collectors.
func RegisterEconomicHealthProm(reg prometheus.Registerer) *EconomicHealthProm {
	if reg == nil {
		return nil
	}
	m := &EconomicHealthProm{
		queuePending: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "economic_queue_pending",
			Help: "Economic work rows pending in the bounded worker queue buckets.",
		}, []string{"queue"}),
		queueProcessing: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "economic_queue_processing",
			Help: "Economic work rows currently being processed.",
		}, []string{"queue"}),
		queueCompleted: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "economic_queue_completed",
			Help: "Economic work rows completed (scan window).",
		}, []string{"queue"}),
		queueProcessed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "economic_queue_processed",
			Help: "Legacy provider-costing rows processed (scan window).",
		}, []string{"queue"}),
		queueFailed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "economic_queue_failed",
			Help: "Economic work rows failed in the bounded worker queue buckets.",
		}, []string{"queue"}),
		queueOldestAge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "economic_queue_oldest_age_seconds",
			Help: "Age in seconds of the oldest outstanding economic work row per queue bucket.",
		}, []string{"queue"}),
		queueMaxAttempt: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "economic_queue_max_attempts",
			Help: "Maximum attempt count observed per bounded queue bucket.",
		}, []string{"queue"}),
		queueRetries: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "economic_queue_retries",
			Help: "Economic retries by bounded queue and reason bucket.",
		}, []string{"queue", "reason"}),
		statements: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "economic_statements",
			Help: "Retained normalized statement lines by bounded outcome.",
		}, []string{"outcome"}),
		discrepancyCount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "economic_discrepancy_count",
			Help: "Retained reconciliation results by bounded plane and status.",
		}, []string{"plane", "status"}),
		discrepancyGross: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "economic_discrepancy_gross_absolute",
			Help: "Gross absolute discrepancy by bounded native currency (no FX conversion).",
		}, []string{"currency"}),
		discrepancyWindows: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "economic_discrepancy_window_rows",
			Help: "Retained reconciliation rows summarized in the bounded window.",
		}),
		discrepancyCurrencyOmitted: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "economic_discrepancy_currency_omitted",
			Help: "Distinct native currency codes omitted from economic_discrepancy_gross_absolute in this snapshot because they are outside the bounded supported-currency allowlist or beyond its series cap. Omitting preserves the no-FX/no-merge rule; no raw code is recorded.",
		}),
	}
	reg.MustRegister(
		m.queuePending, m.queueProcessing, m.queueCompleted, m.queueProcessed, m.queueFailed,
		m.queueOldestAge, m.queueMaxAttempt, m.queueRetries,
		m.statements, m.discrepancyCount, m.discrepancyGross, m.discrepancyWindows,
		m.discrepancyCurrencyOmitted,
	)
	m.seedDefaults()
	return m
}

// seedDefaults materializes one "other" series per bounded non-monetary
// vector so every family exists with deterministic bounded cardinality before
// the first snapshot. discrepancyGross is intentionally not seeded: a
// currency="other" monetary gauge would falsely combine distinct native units.
func (m *EconomicHealthProm) seedDefaults() {
	m.queuePending.WithLabelValues("other")
	m.queueProcessing.WithLabelValues("other")
	m.queueCompleted.WithLabelValues("other")
	m.queueProcessed.WithLabelValues("other")
	m.queueFailed.WithLabelValues("other")
	m.queueOldestAge.WithLabelValues("other")
	m.queueMaxAttempt.WithLabelValues("other")
	m.queueRetries.WithLabelValues("other", "other")
	m.statements.WithLabelValues("other")
	m.discrepancyCount.WithLabelValues("other", "other")
}

// ApplySnapshot resets every vector and republishes the bounded snapshot.
func (m *EconomicHealthProm) ApplySnapshot(s billing.EconomicHealthSnapshot) {
	if m == nil {
		return
	}
	m.reset()
	for _, queue := range s.Queues {
		label := boundEconomicQueueLabel(queue.Queue)
		m.queuePending.WithLabelValues(label).Set(float64(queue.Pending))
		m.queueProcessing.WithLabelValues(label).Set(float64(queue.Processing))
		m.queueCompleted.WithLabelValues(label).Set(float64(queue.Completed))
		m.queueProcessed.WithLabelValues(label).Set(float64(queue.Processed))
		m.queueFailed.WithLabelValues(label).Set(float64(queue.Failed + queue.Other))
		age := queue.OldestAgeSec
		if age < 0 {
			age = 0
		}
		m.queueOldestAge.WithLabelValues(label).Set(age)
		m.queueMaxAttempt.WithLabelValues(label).Set(float64(queue.MaxAttempts))
		for _, retry := range queue.Retries {
			m.queueRetries.WithLabelValues(label, boundEconomicReasonLabel(retry.Reason)).Set(float64(retry.Count))
		}
	}
	m.statements.WithLabelValues("matched").Set(float64(s.Statements.Matched))
	m.statements.WithLabelValues("unmatched").Set(float64(s.Statements.Unmatched))
	m.statements.WithLabelValues("other").Set(float64(s.Statements.Other))
	for _, status := range s.Discrepancies.ByQuantity {
		m.discrepancyCount.WithLabelValues("quantity", boundEconomicStatusLabel(status.Status)).Set(float64(status.Count))
	}
	for _, status := range s.Discrepancies.ByMonetary {
		m.discrepancyCount.WithLabelValues("monetary", boundEconomicStatusLabel(status.Status)).Set(float64(status.Count))
	}
	// The exact per-currency aggregate is computed with big rationals in the
	// domain; the Prometheus gauge carries a float64 operational projection of
	// the same bounded dimension. Currencies are never converted to one
	// another and never summed across units: each supported native currency
	// remains its own series. Codes outside the fixed allowlist (or beyond the
	// hard series cap) are dropped and only counted, never relabelled "other".
	emittedCurrencies := 0
	omittedCurrencies := map[string]struct{}{}
	for _, total := range s.Discrepancies.Gross {
		label, ok := economicHealthSupportedCurrency(total.Currency)
		if !ok || emittedCurrencies >= economicHealthMaxCurrencySeries {
			omittedCurrencies[strings.TrimSpace(total.Currency)] = struct{}{}
			continue
		}
		value := 0.0
		if rat, ok := new(big.Rat).SetString(total.Amount); ok {
			if f, _ := rat.Float64(); f > 0 {
				value = f
			}
		}
		m.discrepancyGross.WithLabelValues(label).Set(value)
		emittedCurrencies++
	}
	m.discrepancyCurrencyOmitted.Set(float64(len(omittedCurrencies)))
	m.discrepancyWindows.Set(float64(s.WindowRows))
}

func (m *EconomicHealthProm) reset() {
	for _, vec := range []*prometheus.GaugeVec{
		m.queuePending, m.queueProcessing, m.queueCompleted, m.queueProcessed, m.queueFailed,
		m.queueOldestAge, m.queueMaxAttempt, m.queueRetries,
		m.statements, m.discrepancyCount, m.discrepancyGross,
	} {
		vec.Reset()
	}
	m.seedDefaults()
}

var economicHealthKnownQueues = map[string]bool{
	"customer": true, "provider": true, "provider_legacy": true,
	"customer_rating": true, "provider_rating": true, "reconciliation": true,
}

func boundEconomicQueueLabel(v string) string {
	v = strings.TrimSpace(v)
	if economicHealthKnownQueues[v] {
		return v
	}
	return "other"
}

var economicHealthKnownReasons = map[string]bool{
	"unclassified": true, "transient_failure": true, "rater_failure": true,
	"reconciler_failure": true, "persistence_failure": true, "dependency_pending": true,
	"lease_expired": true, "permanent_failure": true, "other": true,
}

func boundEconomicReasonLabel(v string) string {
	v = strings.TrimSpace(v)
	if economicHealthKnownReasons[v] {
		return v
	}
	return "other"
}

var economicHealthKnownStatuses = map[string]bool{
	"matched": true, "within_tolerance": true, "discrepant": true, "partial": true,
	"incomparable": true, "missing_local": true, "missing_provider": true,
	"pending_statement": true, "conflict": true, "complete": true, "other": true,
}

func boundEconomicStatusLabel(v string) string {
	v = strings.TrimSpace(v)
	if economicHealthKnownStatuses[v] {
		return v
	}
	return "other"
}

// economicHealthSupportedCurrencies is the fixed, explicit, reviewable
// operational allowlist of native monetary units that may appear on
// economic_discrepancy_gross_absolute. It is deliberately finite and
// code-reviewed: any other code is dropped from the monetary gauge and only
// counted by lip_economic_discrepancy_currency_omitted. Growing this set is a
// reviewable source change, never a runtime/store-controlled decision, so the
// currency label vocabulary can never grow with hostile or retained data.
var economicHealthSupportedCurrencies = map[string]struct{}{
	"USD": {}, "EUR": {}, "GBP": {}, "JPY": {}, "CNY": {}, "CHF": {},
	"CAD": {}, "AUD": {}, "NZD": {}, "HKD": {}, "SGD": {}, "SEK": {},
	"NOK": {}, "DKK": {}, "PLN": {}, "CZK": {}, "HUF": {}, "RON": {},
	"TRY": {}, "ZAR": {}, "INR": {}, "KRW": {}, "TWD": {}, "BRL": {},
	"MXN": {}, "AED": {}, "SAR": {}, "ILS": {}, "THB": {}, "MYR": {},
	"IDR": {}, "PHP": {}, "VND": {}, "NGN": {}, "KES": {}, "EGP": {},
	"COP": {}, "ARS": {}, "CLP": {}, "PEN": {},
}

// economicHealthMaxCurrencySeries is the explicit hard cap on the number of
// native-currency series published for economic_discrepancy_gross_absolute per
// snapshot. It is a defensive bound independent of the allowlist size, so even
// a future allowlist extension cannot grow the family without a reviewable
// source change.
const economicHealthMaxCurrencySeries = 64

// economicHealthSupportedCurrency reports whether v is a supported native
// monetary unit and returns its exact label. Unlike the queue/reason/status
// bucketers it never maps unknown input to "other": distinct monetary units
// must not be merged, so the caller drops unsupported codes and counts them
// through the label-free omission gauge instead.
func economicHealthSupportedCurrency(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if _, ok := economicHealthSupportedCurrencies[v]; ok {
		return v, true
	}
	return "", false
}
