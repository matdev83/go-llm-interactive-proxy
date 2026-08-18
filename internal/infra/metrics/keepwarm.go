package metrics

import (
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/keepwarm"
	"github.com/prometheus/client_golang/prometheus"
)

// KeepwarmProm exports only bounded keep-warm state and finite event labels.
// It retains no provider handles, cache identities, prompts, or session IDs.
type KeepwarmProm struct {
	mu      sync.RWMutex
	manager *keepwarm.Manager

	activeEpochs  *prometheus.Desc
	activeTargets *prometheus.Desc
	events        *prometheus.Desc
}

// RegisterKeepwarmProm registers the process collector. The active manager can
// be replaced at generation publication without replacing the metric family.
func RegisterKeepwarmProm(reg prometheus.Registerer) *KeepwarmProm {
	p := &KeepwarmProm{
		activeEpochs: prometheus.NewDesc(
			"lip_prompt_cache_keepwarm_active_epochs", "Active keep-warm idle epochs.", nil, nil,
		),
		activeTargets: prometheus.NewDesc(
			"lip_prompt_cache_keepwarm_active_targets", "Active keep-warm residency targets.", nil, nil,
		),
		events: prometheus.NewDesc(
			"lip_prompt_cache_keepwarm_events_total", "Keep-warm events by bounded event name.", []string{"event"}, nil,
		),
	}
	reg.MustRegister(p)
	return p
}

// SetManager changes the generation whose state is exported.
func (p *KeepwarmProm) SetManager(manager *keepwarm.Manager) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.manager = manager
	p.mu.Unlock()
}

func (p *KeepwarmProm) Describe(ch chan<- *prometheus.Desc) {
	if p == nil {
		return
	}
	ch <- p.activeEpochs
	ch <- p.activeTargets
	ch <- p.events
}

func (p *KeepwarmProm) Collect(ch chan<- prometheus.Metric) {
	if p == nil {
		return
	}
	p.mu.RLock()
	manager := p.manager
	p.mu.RUnlock()
	if manager == nil {
		return
	}
	snapshot := manager.Metrics()
	ch <- prometheus.MustNewConstMetric(p.activeEpochs, prometheus.GaugeValue, float64(snapshot.ActiveEpochs))
	ch <- prometheus.MustNewConstMetric(p.activeTargets, prometheus.GaugeValue, float64(snapshot.ActiveTargets))
	for event, count := range snapshot.Events {
		if !keepwarmMetricEventAllowed(event) {
			continue
		}
		ch <- prometheus.MustNewConstMetric(p.events, prometheus.CounterValue, float64(count), event)
	}
}

func keepwarmMetricEventAllowed(event string) bool {
	switch event {
	case "armed", "disabled_global", "disabled_session", "uncommitted", "no_os_command",
		"invalid_lineage", "revision_exhausted", "no_eligible_target", "generation_quiescing",
		"expired", "unsafe_window", "no_schedule", "budget_unknown", "budget_exhausted",
		"capacity", "cancel_foreground", "cancel_session_end", "cancel_disabled",
		"cancel_arm_replacement", "cancel_quiesce", "cancel_exhausted",
		"stale_result", "control_error", "renewed",
		"still_resident", "cold_recreated", "stale", "unsupported", "control_failed",
		"release_dropped":
		return true
	default:
		return false
	}
}

var _ prometheus.Collector = (*KeepwarmProm)(nil)
