package metrics

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/prometheus/client_golang/prometheus"
)

// ConversationViewProm holds Prometheus collectors for bounded conversation-view diagnostics.
// All labels are bounded enums (operation, placement, policy, stage), never OverlayID/ALegID/digest/plaintext.
type ConversationViewProm struct {
	filtered       *prometheus.CounterVec
	injected       *prometheus.CounterVec
	mutations      *prometheus.CounterVec
	anchorFallback *prometheus.CounterVec
	anchorFailure  *prometheus.CounterVec
	projectionFail *prometheus.CounterVec
	discontinuity  *prometheus.CounterVec
}

// RegisterConversationViewProm registers lip_conversation_view_* series on reg.
func RegisterConversationViewProm(reg prometheus.Registerer) *ConversationViewProm {
	m := &ConversationViewProm{
		filtered: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "conversation_view_filtered_messages_total",
				Help:      "Messages filtered as never_backend (bounded stage label only).",
			},
			[]string{"stage"},
		),
		injected: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "conversation_view_steering_injections_total",
				Help:      "Steering injections by placement (bounded placement label).",
			},
			[]string{"placement"},
		),
		mutations: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "conversation_view_steering_mutations_total",
				Help:      "Steering mutations by operation and placement (bounded operation, placement).",
			},
			[]string{"operation", "placement"},
		),
		anchorFallback: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "conversation_view_anchor_fallback_total",
				Help:      "Anchor fallback occurrences by stage and policy (bounded stage, policy).",
			},
			[]string{"stage", "policy"},
		),
		anchorFailure: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "conversation_view_anchor_failure_total",
				Help:      "Anchor failure occurrences by policy (bounded policy).",
			},
			[]string{"policy"},
		),
		projectionFail: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "conversation_view_projection_failures_total",
				Help:      "Projection/reassert failures by stage (bounded stage).",
			},
			[]string{"stage"},
		),
		discontinuity: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "conversation_view_cache_discontinuities_total",
				Help:      "Explicit cache discontinuities by operation and placement (bounded).",
			},
			[]string{"operation", "placement"},
		),
	}
	reg.MustRegister(m.filtered, m.injected, m.mutations, m.anchorFallback, m.anchorFailure, m.projectionFail, m.discontinuity)
	return m
}

type conversationViewSink struct {
	p *ConversationViewProm
}

// NewConversationViewSink adapts ConversationViewProm to conversationview.Observer.
func NewConversationViewSink(p *ConversationViewProm) conversationview.Observer {
	if p == nil {
		return nil
	}
	return &conversationViewSink{p: p}
}

func sanitizePlacement(k conversationview.PlacementKind) string {
	switch k {
	case conversationview.PlacementStablePrefix, conversationview.PlacementAfterMessage:
		return string(k)
	default:
		return "unknown"
	}
}

func sanitizeOperation(k conversationview.CacheDiscontinuityKind) string {
	switch k {
	case conversationview.CacheDiscontinuityCreate, conversationview.CacheDiscontinuityReplace, conversationview.CacheDiscontinuityMove, conversationview.CacheDiscontinuityDeactivate:
		return string(k)
	default:
		return "unknown"
	}
}

func sanitizePolicy(p conversationview.AnchorMissingPolicy) string {
	switch p {
	case conversationview.AnchorStablePrefixFallback, conversationview.AnchorFailClosed:
		return string(p)
	default:
		return "unknown"
	}
}

func sanitizeStage(s string) string {
	switch s {
	case conversationview.StageEarly, conversationview.StageFinal, conversationview.StageSDKResolve:
		return s
	default:
		return "unknown"
	}
}

func (s *conversationViewSink) OnProjection(stage string, summary conversationview.ProjectionSummary) {
	if s == nil || s.p == nil {
		return
	}
	st := sanitizeStage(stage)
	if summary.FilteredCount > 0 {
		s.p.filtered.WithLabelValues(st).Add(float64(summary.FilteredCount))
	}
	if summary.StablePrefixCount > 0 {
		s.p.injected.WithLabelValues(string(conversationview.PlacementStablePrefix)).Add(float64(summary.StablePrefixCount))
	}
	if summary.AfterMessageCount > 0 {
		s.p.injected.WithLabelValues(string(conversationview.PlacementAfterMessage)).Add(float64(summary.AfterMessageCount))
	}
}

func (s *conversationViewSink) OnProjectionFailure(stage string) {
	if s == nil || s.p == nil {
		return
	}
	s.p.projectionFail.WithLabelValues(sanitizeStage(stage)).Inc()
}

func (s *conversationViewSink) OnAnchorFallback(stage string, policy conversationview.AnchorMissingPolicy) {
	if s == nil || s.p == nil {
		return
	}
	s.p.anchorFallback.WithLabelValues(sanitizeStage(stage), sanitizePolicy(policy)).Inc()
}

func (s *conversationViewSink) OnAnchorFailure(policy conversationview.AnchorMissingPolicy) {
	if s == nil || s.p == nil {
		return
	}
	s.p.anchorFailure.WithLabelValues(sanitizePolicy(policy)).Inc()
}

func (s *conversationViewSink) OnSteeringMutation(kind conversationview.CacheDiscontinuityKind, placement conversationview.PlacementKind) {
	if s == nil || s.p == nil {
		return
	}
	op := sanitizeOperation(kind)
	pl := sanitizePlacement(placement)
	s.p.mutations.WithLabelValues(op, pl).Inc()
	s.p.discontinuity.WithLabelValues(op, pl).Inc()
}
