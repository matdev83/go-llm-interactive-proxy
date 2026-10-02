package sessionclassification

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// PrometheusCollector exports bounded process state for session classification.
// The ready gauge describes whether an enabled generation has initialized the
// shared store; it carries no session or request labels.
type PrometheusCollector struct {
	mu         sync.RWMutex
	storeReady bool
	storeDesc  *prometheus.Desc
}

// NewPrometheusCollector constructs an unregistered feature-owned collector.
func NewPrometheusCollector() *PrometheusCollector {
	return &PrometheusCollector{
		storeDesc: prometheus.NewDesc(
			"lip_session_classification_store_ready",
			"Whether the process-owned session classification store has been initialized.",
			nil,
			nil,
		),
	}
}

func (c *PrometheusCollector) setStoreReady(ready bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.storeReady = ready
	c.mu.Unlock()
}

func (c *PrometheusCollector) Describe(ch chan<- *prometheus.Desc) {
	if c == nil || c.storeDesc == nil {
		return
	}
	ch <- c.storeDesc
}

func (c *PrometheusCollector) Collect(ch chan<- prometheus.Metric) {
	if c == nil || c.storeDesc == nil {
		return
	}
	c.mu.RLock()
	ready := c.storeReady
	c.mu.RUnlock()
	value := 0.0
	if ready {
		value = 1
	}
	ch <- prometheus.MustNewConstMetric(c.storeDesc, prometheus.GaugeValue, value)
}

var _ prometheus.Collector = (*PrometheusCollector)(nil)
