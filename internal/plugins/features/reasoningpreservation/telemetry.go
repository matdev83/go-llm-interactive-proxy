package reasoningpreservation

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Telemetry accumulates content-safe aggregate outcome counters for one feature instance.
// All byte counters are content-free, outcome-whitelisted via isKnownOutcome, and
// individually clamped to hard ceilings to prevent unbounded aggregation.
// Concurrency: sync.Map + atomic.Int64 ensures lock-free per-outcome updates.
// Per-outcome raw is stored once in rawBytes (dedicated); BytesSnapshot is an alias for backward compat and returns RawBytesSnapshot.
type Telemetry struct {
	outcomes     sync.Map // SafeOutcome -> *atomic.Int64 (count per outcome)
	bytes        sync.Map // deprecated: retained for wire compat but no longer double-counted; BytesSnapshot aliases RawBytesSnapshot
	rawBytes     sync.Map // SafeOutcome -> *atomic.Int64 dedicated raw bytes per outcome (bounded to HardRawOutputCeiling)
	decodedBytes sync.Map // SafeOutcome -> *atomic.Int64 decoded surrogate bytes per outcome (bounded to HardCompressionMaxSurrogateBytes)
	savedBytes   sync.Map // SafeOutcome -> *atomic.Int64 saved bytes per outcome (bounded to HardRawOutputCeiling)
	sourceBytes  sync.Map // SafeOutcome -> *atomic.Int64 source bytes per outcome (bounded to HardRawOutputCeiling)
	latencyNanos sync.Map // SafeOutcome -> *atomic.Int64 total latency nanos per outcome (bounded aggregation)
}

// CompressionMeasurements is a content-free snapshot of per-outcome byte measurements.
type CompressionMeasurements struct {
	Counts       map[SafeOutcome]int64
	RawBytes     map[SafeOutcome]int64
	DecodedBytes map[SafeOutcome]int64
	SavedBytes   map[SafeOutcome]int64
}

// ShadowEvaluation is the bounded content-free evaluation snapshot for shadow mode.
// It aggregates source/raw/decoded/saved bytes and ratios, plus per-outcome counts and latency.
// No economic/money calculation is performed; billing remains authoritative via existing
// auxiliary BillingCallID / usage surfaces. All numeric fields are bounded.
type ShadowEvaluation struct {
	Counts       map[SafeOutcome]int64
	SourceBytes  map[SafeOutcome]int64
	RawBytes     map[SafeOutcome]int64
	DecodedBytes map[SafeOutcome]int64
	SavedBytes   map[SafeOutcome]int64
	Latency      map[SafeOutcome]time.Duration
	TotalCount   int64
	TotalSource  int64
	TotalRaw     int64
	TotalDecoded int64
	TotalSaved   int64
	// Ratio is hypothetical saved/source when TotalSource>0, else 0. No claim of semantic equivalence.
	SavingsRatio     float64
	CompressionRatio float64
	// AvgLatency is TotalLatency / TotalCount for outcomes with latency if TotalCount>0.
	AvgLatency time.Duration
}

func NewTelemetry() *Telemetry {
	return &Telemetry{}
}

func (t *Telemetry) Record(outcome SafeOutcome, counts map[string]int) {
	if t == nil || !isKnownOutcome(outcome) {
		return
	}
	v, _ := t.outcomes.LoadOrStore(outcome, &atomic.Int64{})
	n, ok := v.(*atomic.Int64)
	if !ok || n == nil {
		return
	}
	n.Add(1)
	if counts != nil {
		if b, ok := counts["bytes"]; ok && b > 0 {
			if b > HardRawOutputCeiling {
				b = HardRawOutputCeiling
			}
			vb, _ := t.rawBytes.LoadOrStore(outcome, &atomic.Int64{})
			if nb, ok := vb.(*atomic.Int64); ok && nb != nil {
				nb.Add(int64(b))
			}
		}
		if b, ok := counts["sourceBytes"]; ok && b > 0 {
			if b > HardRawOutputCeiling {
				b = HardRawOutputCeiling
			}
			vb, _ := t.sourceBytes.LoadOrStore(outcome, &atomic.Int64{})
			if nb, ok := vb.(*atomic.Int64); ok && nb != nil {
				nb.Add(int64(b))
			}
		}
	}
}

// RecordCompression records a compression-safe outcome with content-free byte count.
// It records raw bytes once in the dedicated rawBytes map (single count, bounded to HardRawOutputCeiling).
// BytesSnapshot is an alias to RawBytesSnapshot for backward compat.
// New code should prefer RecordCompressionMeasurement or RecordShadowMeasurement.
func (t *Telemetry) RecordCompression(outcome SafeOutcome, rawBytes int) {
	if t == nil || !isKnownOutcome(outcome) {
		return
	}
	m := map[string]int{"count": 1}
	if rawBytes > 0 {
		if rawBytes > HardRawOutputCeiling {
			rawBytes = HardRawOutputCeiling
		}
		m["bytes"] = rawBytes
	}
	t.Record(outcome, m)
}

// RecordCompressionMeasurement records an outcome with explicit raw/decoded/saved bytes, each clamped.
// Raw is stored once in rawBytes; Decoded bounded to HardCompressionMaxSurrogateBytes, saved to HardRawOutputCeiling.
// It is content-free and outcome-whitelisted via isKnownOutcome.
func (t *Telemetry) RecordCompressionMeasurement(outcome SafeOutcome, rawBytes, decodedBytes, savedBytes int) {
	if t == nil || !isKnownOutcome(outcome) {
		return
	}
	v, _ := t.outcomes.LoadOrStore(outcome, &atomic.Int64{})
	n, ok := v.(*atomic.Int64)
	if !ok || n == nil {
		return
	}
	n.Add(1)
	if rawBytes > 0 {
		if rawBytes > HardRawOutputCeiling {
			rawBytes = HardRawOutputCeiling
		}
		vb, _ := t.rawBytes.LoadOrStore(outcome, &atomic.Int64{})
		if nb, ok := vb.(*atomic.Int64); ok && nb != nil {
			nb.Add(int64(rawBytes))
		}
	}
	if decodedBytes > 0 {
		if decodedBytes > HardCompressionMaxSurrogateBytes {
			decodedBytes = HardCompressionMaxSurrogateBytes
		}
		vb, _ := t.decodedBytes.LoadOrStore(outcome, &atomic.Int64{})
		if nb, ok := vb.(*atomic.Int64); ok && nb != nil {
			nb.Add(int64(decodedBytes))
		}
	}
	if savedBytes > 0 {
		if savedBytes > HardRawOutputCeiling {
			savedBytes = HardRawOutputCeiling
		}
		vb, _ := t.savedBytes.LoadOrStore(outcome, &atomic.Int64{})
		if nb, ok := vb.(*atomic.Int64); ok && nb != nil {
			nb.Add(int64(savedBytes))
		}
	}
}

// RecordShadowMeasurement records a full shadow evaluation sample with source/raw/decoded/saved and latency.
// All byte values are bounded; latency is bounded to 24h total per outcome aggregation to avoid overflow.
// No money calculation. Content-free via isKnownOutcome.
func (t *Telemetry) RecordShadowMeasurement(outcome SafeOutcome, sourceBytes, rawBytes, decodedBytes, savedBytes int, latency time.Duration) {
	if t == nil || !isKnownOutcome(outcome) {
		return
	}
	t.RecordCompressionMeasurement(outcome, rawBytes, decodedBytes, savedBytes)
	if sourceBytes > 0 {
		if sourceBytes > HardRawOutputCeiling {
			sourceBytes = HardRawOutputCeiling
		}
		vb, _ := t.sourceBytes.LoadOrStore(outcome, &atomic.Int64{})
		if nb, ok := vb.(*atomic.Int64); ok && nb != nil {
			nb.Add(int64(sourceBytes))
		}
	}
	if latency > 0 {
		// Cap per-record latency to 30s (HardCompressionTimeout) to bound aggregation.
		if latency > HardCompressionTimeout {
			latency = HardCompressionTimeout
		}
		vb, _ := t.latencyNanos.LoadOrStore(outcome, &atomic.Int64{})
		if nb, ok := vb.(*atomic.Int64); ok && nb != nil {
			nb.Add(int64(latency))
		}
	}
}

func (t *Telemetry) Snapshot() map[SafeOutcome]int64 {
	out := make(map[SafeOutcome]int64)
	if t == nil {
		return out
	}
	t.outcomes.Range(func(key, value any) bool {
		o, ok := key.(SafeOutcome)
		if !ok {
			return true
		}
		n, ok := value.(*atomic.Int64)
		if !ok || n == nil {
			return true
		}
		if c := n.Load(); c > 0 {
			out[o] = c
		}
		return true
	})
	return out
}

// BytesSnapshot returns aggregate raw bytes per outcome (alias to RawBytesSnapshot, single count, legacy compat).
func (t *Telemetry) BytesSnapshot() map[SafeOutcome]int64 {
	return t.RawBytesSnapshot()
}

// RawBytesSnapshot returns raw bytes per outcome (dedicated, bounded).
func (t *Telemetry) RawBytesSnapshot() map[SafeOutcome]int64 {
	out := make(map[SafeOutcome]int64)
	if t == nil {
		return out
	}
	t.rawBytes.Range(func(key, value any) bool {
		o, ok := key.(SafeOutcome)
		if !ok {
			return true
		}
		n, ok := value.(*atomic.Int64)
		if !ok || n == nil {
			return true
		}
		if c := n.Load(); c > 0 {
			out[o] = c
		}
		return true
	})
	return out
}

// DecodedBytesSnapshot returns decoded bytes per outcome.
func (t *Telemetry) DecodedBytesSnapshot() map[SafeOutcome]int64 {
	out := make(map[SafeOutcome]int64)
	if t == nil {
		return out
	}
	t.decodedBytes.Range(func(key, value any) bool {
		o, ok := key.(SafeOutcome)
		if !ok {
			return true
		}
		n, ok := value.(*atomic.Int64)
		if !ok || n == nil {
			return true
		}
		if c := n.Load(); c > 0 {
			out[o] = c
		}
		return true
	})
	return out
}

// SavedBytesSnapshot returns saved bytes per outcome.
func (t *Telemetry) SavedBytesSnapshot() map[SafeOutcome]int64 {
	out := make(map[SafeOutcome]int64)
	if t == nil {
		return out
	}
	t.savedBytes.Range(func(key, value any) bool {
		o, ok := key.(SafeOutcome)
		if !ok {
			return true
		}
		n, ok := value.(*atomic.Int64)
		if !ok || n == nil {
			return true
		}
		if c := n.Load(); c > 0 {
			out[o] = c
		}
		return true
	})
	return out
}

// SourceBytesSnapshot returns source bytes per outcome (bounded).
func (t *Telemetry) SourceBytesSnapshot() map[SafeOutcome]int64 {
	out := make(map[SafeOutcome]int64)
	if t == nil {
		return out
	}
	t.sourceBytes.Range(func(key, value any) bool {
		o, ok := key.(SafeOutcome)
		if !ok {
			return true
		}
		n, ok := value.(*atomic.Int64)
		if !ok || n == nil {
			return true
		}
		if c := n.Load(); c > 0 {
			out[o] = c
		}
		return true
	})
	return out
}

// LatencySnapshot returns total latency per outcome (bounded).
func (t *Telemetry) LatencySnapshot() map[SafeOutcome]time.Duration {
	out := make(map[SafeOutcome]time.Duration)
	if t == nil {
		return out
	}
	t.latencyNanos.Range(func(key, value any) bool {
		o, ok := key.(SafeOutcome)
		if !ok {
			return true
		}
		n, ok := value.(*atomic.Int64)
		if !ok || n == nil {
			return true
		}
		if c := n.Load(); c > 0 {
			out[o] = time.Duration(c)
		}
		return true
	})
	return out
}

// CompressionMeasurementsSnapshot returns per-outcome counts and byte measurements.
func (t *Telemetry) CompressionMeasurementsSnapshot() CompressionMeasurements {
	if t == nil {
		return CompressionMeasurements{
			Counts:       map[SafeOutcome]int64{},
			RawBytes:     map[SafeOutcome]int64{},
			DecodedBytes: map[SafeOutcome]int64{},
			SavedBytes:   map[SafeOutcome]int64{},
		}
	}
	return CompressionMeasurements{
		Counts:       t.Snapshot(),
		RawBytes:     t.RawBytesSnapshot(),
		DecodedBytes: t.DecodedBytesSnapshot(),
		SavedBytes:   t.SavedBytesSnapshot(),
	}
}

// ShadowEvaluationSnapshot returns a bounded evaluation computing source/raw/decoded/saved totals and ratios.
// Ratios are hypothetical savings only; no money calculation; latency is averaged if available.
func (t *Telemetry) ShadowEvaluationSnapshot() ShadowEvaluation {
	if t == nil {
		return ShadowEvaluation{
			Counts:       map[SafeOutcome]int64{},
			SourceBytes:  map[SafeOutcome]int64{},
			RawBytes:     map[SafeOutcome]int64{},
			DecodedBytes: map[SafeOutcome]int64{},
			SavedBytes:   map[SafeOutcome]int64{},
			Latency:      map[SafeOutcome]time.Duration{},
		}
	}
	counts := t.Snapshot()
	src := t.SourceBytesSnapshot()
	raw := t.RawBytesSnapshot()
	dec := t.DecodedBytesSnapshot()
	saved := t.SavedBytesSnapshot()
	lat := t.LatencySnapshot()
	var totalCount, totalSource, totalRaw, totalDecoded, totalSaved int64
	var totalLatency time.Duration
	for _, c := range counts {
		totalCount = saturatingAddInt64(totalCount, c)
	}
	for _, v := range src {
		totalSource = saturatingAddInt64(totalSource, v)
	}
	for _, v := range raw {
		totalRaw = saturatingAddInt64(totalRaw, v)
	}
	for _, v := range dec {
		totalDecoded = saturatingAddInt64(totalDecoded, v)
	}
	for _, v := range saved {
		totalSaved = saturatingAddInt64(totalSaved, v)
	}
	for _, v := range lat {
		totalLatency = saturatingAddDuration(totalLatency, v)
	}
	// Totals already saturating, ratios clamp 0..1.
	var savingsRatio, compressionRatio float64
	if totalSource > 0 {
		savingsRatio = float64(totalSaved) / float64(totalSource)
		compressionRatio = float64(totalDecoded) / float64(totalSource)
		if savingsRatio < 0 {
			savingsRatio = 0
		}
		if savingsRatio > 1 {
			savingsRatio = 1
		}
		if compressionRatio < 0 {
			compressionRatio = 0
		}
		if compressionRatio > 1 {
			compressionRatio = 1
		}
	}
	var avgLatency time.Duration
	if totalCount > 0 {
		avgLatency = time.Duration(int64(totalLatency) / totalCount)
	}
	return ShadowEvaluation{
		Counts:           counts,
		SourceBytes:      src,
		RawBytes:         raw,
		DecodedBytes:     dec,
		SavedBytes:       saved,
		Latency:          lat,
		TotalCount:       totalCount,
		TotalSource:      totalSource,
		TotalRaw:         totalRaw,
		TotalDecoded:     totalDecoded,
		TotalSaved:       totalSaved,
		SavingsRatio:     savingsRatio,
		CompressionRatio: compressionRatio,
		AvgLatency:       avgLatency,
	}
}

func saturatingAddInt64(a, b int64) int64 {
	if b > 0 && a > (1<<63-1)-b {
		return 1<<63 - 1
	}
	if b < 0 && a < (-1<<63)-b {
		return -1 << 63
	}
	return a + b
}

func saturatingAddDuration(a, b time.Duration) time.Duration {
	ai, bi := int64(a), int64(b)
	if bi > 0 && ai > (1<<63-1)-bi {
		return time.Duration(1<<63 - 1)
	}
	if bi < 0 && ai < (-1<<63)-bi {
		return time.Duration(-1 << 63)
	}
	return a + b
}

// SafeInventory is the process-local, content-safe diagnostics projection for one enabled instance.
type SafeInventory struct {
	Enabled            bool             `json:"enabled"`
	Action             string           `json:"action"`
	CatalogVersion     string           `json:"catalog_version,omitempty"`
	RuleIDs            []string         `json:"rule_ids,omitempty"`
	RuleCount          int              `json:"rule_count"`
	TTL                string           `json:"ttl"`
	MaxTurnsPerSession int              `json:"max_turns_per_session"`
	MaxBytesPerTurn    int              `json:"max_reasoning_bytes_per_turn"`
	MaxSessionBytes    int              `json:"max_session_bytes"`
	ProcessLocal       bool             `json:"process_local"`
	AggregateCounters  map[string]int64 `json:"aggregate_counters"`
}

// BuildSafeInventory projects config + aggregate counters without payloads, anchors, or session partitions.
func BuildSafeInventory(cfg Config, tel *Telemetry) SafeInventory {
	if strings.TrimSpace(cfg.Action) == "" {
		return SafeInventory{AggregateCounters: map[string]int64{}}
	}
	ids := make([]string, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		if id := sanitizeRuleID(r.ID); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	agg := map[string]int64{}
	if tel != nil {
		for o, n := range tel.Snapshot() {
			agg[string(o)] = n
		}
	}
	inv := SafeInventory{
		Enabled:            true,
		Action:             cfg.Action,
		RuleIDs:            ids,
		RuleCount:          len(ids),
		TTL:                cfg.State.TTL.String(),
		MaxTurnsPerSession: cfg.State.MaxTurnsPerSession,
		MaxBytesPerTurn:    cfg.State.MaxReasoningBytesPerTurn,
		MaxSessionBytes:    cfg.State.MaxSessionBytes,
		ProcessLocal:       true,
		AggregateCounters:  agg,
	}
	if cfg.UseBuiltinCatalog {
		inv.CatalogVersion = BuiltinCatalogVersion
	}
	return inv
}
