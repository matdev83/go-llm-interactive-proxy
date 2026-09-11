package metrics

import (
	"database/sql"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	accountingobs "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/observability"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/prometheus/client_golang/prometheus"
)

// Bundle is a dedicated Prometheus registry plus handles used by stdhttp and runtimebundle.
type Bundle struct {
	Registry *prometheus.Registry
	HTTP     *HTTPMetrics
	Executor *ExecutorProm
	// SecureSession is non-nil when metrics are enabled; secure-session begin/recorder/touch series.
	SecureSession *SecureSessionProm
	// ExtensionStages is non-nil when metrics are enabled; used for extension pipeline histograms/counters.
	ExtensionStages     *ExtensionStageProm
	SecretGuard         *SecretGuardProm
	AuthorityStages     *AuthorityStageProm
	Upstream            *UpstreamProm
	TokenAccounting     *TokenAccountingProm
	PostgresPool        *PostgresPoolProm
	TerminalWork        *TerminalWorkProm
	Reload              *ReloadProm
	GeoIP               *GeoIPProm
	ConversationView    *ConversationViewProm
	LargePayload        *LargePayloadProm
	sink                runtime.MetricsSink
	tokenAccountingSink *TokenAccountingPromSink
	conversationSink    ConversationViewObserver
	largePayloadSink    largebody.DiagnosticsObserver
}

// NewBundle builds a registry with Go/process, inbound HTTP, executor, and upstream series.
// poolStats snapshots database/sql pool statistics for the postgres pool collector; it
// may be nil when no registry-owned pool exists (the collector then emits zeroed series).
func NewBundle(cfg *config.Config, poolStats func() []sql.DBStats) *Bundle {
	r := NewRegistry()
	exemplars := cfg != nil && cfg.Observability.Metrics.ExemplarsEnabled
	httpm := RegisterHTTPMetrics(r, exemplars)
	exec := RegisterExecutorProm(r)
	ss := RegisterSecureSessionProm(r)
	ext := RegisterExtensionStageProm(r)
	sg := RegisterSecretGuardProm(r)
	auth := RegisterAuthorityStageProm(r)
	up := RegisterUpstreamProm(r, exemplars)
	tok := RegisterTokenAccountingProm(r)
	pg := RegisterPostgresPoolProm(r, poolStats)
	tw := RegisterTerminalWorkProm(r)
	reload := RegisterReloadProm(r)
	geoip := RegisterGeoIPProm(r)
	cv := RegisterConversationViewProm(r)
	lp := RegisterLargePayloadProm(r)
	return &Bundle{
		Registry:            r,
		HTTP:                httpm,
		Executor:            exec,
		SecureSession:       ss,
		ExtensionStages:     ext,
		SecretGuard:         sg,
		AuthorityStages:     auth,
		Upstream:            up,
		TokenAccounting:     tok,
		PostgresPool:        pg,
		TerminalWork:        tw,
		Reload:              reload,
		GeoIP:               geoip,
		ConversationView:    cv,
		LargePayload:        lp,
		sink:                NewExecutorPromSink(exec),
		tokenAccountingSink: NewTokenAccountingPromSink(tok),
		conversationSink:    NewConversationViewSink(cv),
		largePayloadSink:    NewLargePayloadPromSink(lp),
	}
}

// ExecutorSink returns a [runtime.MetricsSink] backed by this bundle's executor metrics.
func (b *Bundle) ExecutorSink() runtime.MetricsSink {
	if b == nil {
		return nil
	}
	return b.sink
}

// ExtensionStageSink returns an [extensions.StageMetrics] backed by this bundle's extension-stage series.
func (b *Bundle) ExtensionStageSink() extensions.StageMetrics {
	if b == nil {
		return nil
	}
	return NewExtensionStageSink(b.ExtensionStages)
}

// SecretGuardDecisionSink returns [extensions.SecretGuardDecisionMetrics] backed by this bundle.
func (b *Bundle) SecretGuardDecisionSink() extensions.SecretGuardDecisionMetrics {
	if b == nil {
		return nil
	}
	return NewSecretGuardDecisionSink(b.SecretGuard)
}

// AuthorityStageSink returns usage-authority stage metrics (req 16.5).
func (b *Bundle) AuthorityStageSink() authorityapp.StageMetrics {
	if b == nil {
		return nil
	}
	return NewAuthorityStageSink(b.AuthorityStages)
}

// SecureSessionMetricsSink returns a [runtime.SecureSessionMetrics] backed by this bundle, or nil when b is nil.
func (b *Bundle) SecureSessionMetricsSink() runtime.SecureSessionMetrics {
	if b == nil {
		return nil
	}
	return SecureSessionMetricsSink(b.SecureSession)
}

// TokenAccountingObservabilitySink returns a Prometheus sink for bounded token-accounting observations.
func (b *Bundle) TokenAccountingObservabilitySink() *TokenAccountingPromSink {
	if b == nil {
		return nil
	}
	return b.tokenAccountingSink
}

// ConversationViewObserver returns a bounded conversation-view observer (nil when metrics disabled).
func (b *Bundle) ConversationViewObserver() ConversationViewObserver {
	if b == nil {
		return nil
	}
	return b.conversationSink
}

// LargePayloadDiagnostics returns a bounded large-payload diagnostics observer.
//
// Unwired-production note (Task 19.1 review finding 3):
// Bundle.LargePayloadDiagnostics and SpoolLedger.SetObserver have no production callers
// yet in server setup because the large payload fast path is default-off. Wiring them
// into production HTTP runtime startup is the obligation of the enablement task.
func (b *Bundle) LargePayloadDiagnostics() largebody.DiagnosticsObserver {
	if b == nil {
		return largebody.NoopDiagnosticsObserver{}
	}
	return b.largePayloadSink
}

var _ interface {
	Record(accountingobs.Observation)
} = (*TokenAccountingPromSink)(nil)
