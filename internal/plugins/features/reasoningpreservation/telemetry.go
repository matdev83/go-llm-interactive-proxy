package reasoningpreservation

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Telemetry accumulates content-safe aggregate outcome counters for one feature instance.
type Telemetry struct {
	outcomes sync.Map // SafeOutcome -> *atomic.Int64
}

func NewTelemetry() *Telemetry {
	return &Telemetry{}
}

func (t *Telemetry) Record(outcome SafeOutcome, _ map[string]int) {
	if t == nil || !isKnownOutcome(outcome) {
		return
	}
	v, _ := t.outcomes.LoadOrStore(outcome, &atomic.Int64{})
	v.(*atomic.Int64).Add(1)
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
	for o, n := range tel.Snapshot() {
		agg[string(o)] = n
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
