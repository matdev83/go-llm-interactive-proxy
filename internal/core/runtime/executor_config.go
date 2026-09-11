package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationprojection"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routeoverride"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingobs "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/observability"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/steering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

// BillingIdentity is the composition-root identity bundle shared by billing
// exposure admission and terminal call-closure stamping so those seams cannot drift.
type BillingIdentity struct {
	AccountID          func(context.Context, lipapi.Call) string
	CustomerPricingRef func(context.Context, lipapi.Call) billing.VersionRef
	ChargePolicyRef    func(context.Context, lipapi.Call) billing.VersionRef
	OperatorRateRef    func(context.Context, string, string) billing.VersionRef

	// WireBounded indicates whether this identity bundle is backed by stock/bounded
	// facts (such as PrincipalSessionIdentity) rather than custom Call-inspecting callbacks.
	// Custom callbacks without an explicit bounded contract remain wire blockers (Requirements 15.6, 19).
	WireBounded bool

	// WireAccountID optionally resolves an account identity directly from bounded scope
	// without constructing or inspecting a lipapi.Call.
	WireAccountID func(context.Context, scope.PrincipalScopeView) string

	// WireCustomerPricingRef optionally resolves the customer pricing snapshot ref from bounded facts.
	WireCustomerPricingRef func(context.Context) billing.VersionRef

	// WireChargePolicyRef optionally resolves the charge policy snapshot ref from bounded facts.
	WireChargePolicyRef func(context.Context) billing.VersionRef
}

// HasCustomCallCallbacks reports whether BillingIdentity has Call-shaped callbacks
// that are NOT certified as wire-bounded (Requirements 15.6, 19).
func (b BillingIdentity) HasCustomCallCallbacks() bool {
	if b.WireBounded {
		return false
	}
	return b.AccountID != nil || b.CustomerPricingRef != nil || b.ChargePolicyRef != nil
}

// CoreRuntime carries continuity store, backends, lifecycle coordination, and clocks.
type CoreRuntime struct {
	Store                b2bua.Store
	Backends             map[string]execbackend.Backend
	ALegLifecycle        *leglifecycle.Coordinator
	Rand                 routing.Rng
	Now                  func() time.Time
	MaxPendingWireEvents int
	StreamRecovery       streamrecovery.Config
	// PromptCacheMaintenance is the optional generation-owned provider-neutral maintenance port.
	PromptCacheMaintenance PromptCacheMaintenance
	// ConversationViewReader is an optional narrow snapshot port. When set,
	// runtime preserves the single-snapshot per-turn invariant (task 3.2).
	ConversationViewReader conversationprojection.Reader
	// ConversationViewTagger is the optional narrow tagger port for local-turn
	// tag-before-release.
	ConversationViewTagger ConversationViewTagger
	// SteeringWriterFactory constructs an authoritative steering writer for a given A-leg.
	SteeringWriterFactory SteeringWriterFactory
	// LargeBodyAssessor evaluates frontend proof for candidate fast-path execution.
	LargeBodyAssessor largebody.LargeBodyAssessor
	// LargeBodyGenerationID is the optional generation identity for largebody execution validation.
	LargeBodyGenerationID string
	// LargeBodyCandidateDomainGeneration is the optional generation identity for late-route proof validation.
	LargeBodyCandidateDomainGeneration string
}

// ConversationViewObserver is optional narrow diagnostics for bounded conversation-view
// projection/anchor/steering metrics. Nil is no-op. Labels are bounded enums only (placement, operation, policy, stage).
type ConversationViewObserver interface {
	OnProjection(stage string, summary conversationprojection.ProjectionSummary)
	OnProjectionFailure(stage string)
	OnAnchorFallback(stage string, policy conversationprojection.AnchorMissingPolicy)
	OnAnchorFailure(policy conversationprojection.AnchorMissingPolicy)
}

// TagRequest is one element of a TagNeverBackend batch.
type TagRequest struct {
	Identity conversationprojection.MessageIdentity `json:"identity"`
	Reason   conversationprojection.ReasonCode      `json:"reason"`
}

func (r TagRequest) Validate() error {
	if err := r.Identity.Validate(); err != nil {
		return fmt.Errorf("%w: %v", conversationprojection.ErrInvalidMessageIdentity, err)
	}
	s := string(r.Reason)
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("%w: reason code is required", conversationprojection.ErrInvalidReasonCode)
	}
	if len(s) > 64 {
		return fmt.Errorf("%w: reason code exceeds 64 bytes", conversationprojection.ErrInvalidReasonCode)
	}
	for _, ch := range s {
		if ch > unicode.MaxASCII {
			return fmt.Errorf("%w: reason code must be ascii", conversationprojection.ErrInvalidReasonCode)
		}
		if ch != '_' && ch != '-' && ch != '.' && (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
			return fmt.Errorf("%w: invalid character %q in reason code", conversationprojection.ErrInvalidReasonCode, ch)
		}
	}
	return nil
}

// TagResult is returned from a successful TagNeverBackend call.
type TagResult struct {
	StateRevision uint64                       `json:"state_revision"`
	Tags          []conversationprojection.Tag `json:"tags"`
}

// ConversationViewTagger is the narrow tagger interface consumed by runtime.
type ConversationViewTagger interface {
	TagNeverBackend(ctx context.Context, aLegID string, tags []TagRequest) (TagResult, error)
}

// SteeringWriterResolver resolves the current call and snapshot for anchor calculation.
type SteeringWriterResolver func(ctx context.Context) (lipapi.Call, conversationprojection.Snapshot, error)

// SteeringWriterFactory constructs an authoritative steering writer for a given A-leg.
type SteeringWriterFactory func(ctx context.Context, aLegID string, resolver SteeringWriterResolver) (steering.Writer, error)

// BillingRuntime carries the runtime seams for two-stage exposure admission and
// durable terminal usage. Token quota and usage-authority stay on AccountingRuntime.
type BillingRuntime struct {
	// BillingCreditGate is the cheap settled-credit screen. Authoritative
	// billing requires it; when wired, runtime invokes it after identity
	// preparation and before route expansion.
	BillingCreditGate BillingCreditGate
	// BillingExposureAdmission is the authoritative operational-exposure seam.
	// When set, it replaces hold admission for this executor generation.
	BillingExposureAdmission BillingExposureAdmission
	// BillingLegObserver receives one recorded current call-leg usage record at terminal ownership.
	// It is observational only and never participates in authorization,
	// settlement, retry, cancellation, or client-visible output.
	BillingLegObserver BillingLegObserver
	TerminalUsageSink  billing.TerminalUsageSink
	// BillingIdentity is the composition identity bundle for exposure admission
	// and terminal call-closure stamping. It contains no hold/authorization identity.
	BillingIdentity BillingIdentity
}

// hasTerminalSink reports whether the process-local terminal sink is wired.
func (e *Executor) hasTerminalSink() bool {
	return e != nil && e.TerminalUsageSink != nil
}

// hasTerminalCallSink reports whether the terminal sink can persist call closure.
// The leg and call records share one authoritative sink.
func (e *Executor) hasTerminalCallSink() bool {
	return e.hasTerminalSink()
}

// RoutingRuntime carries selector parsing, planning, negotiation, and affinity policy.
type RoutingRuntime struct {
	MaxAttempts             int
	DefaultBackend          string
	SelectorAliases         *routing.AliasResolver
	CapsResolver            capabilities.Resolver
	CatalogResolver         CatalogResolver
	EligibilityResolver     EligibilityResolver
	RequestTokenEstimator   RequestTokenEstimator
	CandidateHealth         policy.CandidateHealth
	RouteObserver           lipsdk.RouteObserver
	AffinityStore           affinity.Store
	AffinityMissingIdentity affinity.MissingIdentityPolicy
	TransportFallbackPolicy lipapi.TransportFallbackPolicy
	RouteOverrideReader     routeoverride.Reader

	ExecutionCompositionPolicy config.ExecutionCompositionPolicy
	BackendExecutionResolver   routing.BackendExecutionResolver
}

// SecurityRuntime carries secure-session gates, auth events, and session audit policy.
type SecurityRuntime struct {
	SecureSession                           *app.Manager
	SyntheticLocalPrincipal                 bool
	SecureSessionRecorder                   app.GateRecording
	SecureSessionRecordingMandatory         bool
	SessionDenialMapper                     func(error) error
	SecureSessionMetrics                    SecureSessionMetrics
	SecureSessionRequireWorkspaceID         bool
	SecureSessionWorkspaceResolveFailClosed bool
	AuthEvents                              *auth.EventDispatcher
	SessionAuditPolicy                      auth.SessionAuditPolicy
}

// AccountingRuntime carries token-accounting admission, usage reconstruction, and ledger hooks.
type AccountingRuntime struct {
	Preflight                    *accountingpreflight.Checker
	StreamUsage                  *accountingstream.Reconstructor
	TokenAccountingObservability *accountingobs.Stats
	AdminCountService            *accountingapp.Service
	UsageAuthority               UsageAuthorityService
	UsageAuthorityCleanupTimeout time.Duration
	// ConcurrencyProvider is the optional Phase 8 logical-request lease authority.
	ConcurrencyProvider authority.ConcurrencyProvider
	// ConcurrencyLeaseTTL / ConcurrencyRenewBefore are defaults used by heartbeat
	// when the admit decision omits rule-level values.
	ConcurrencyLeaseTTL    time.Duration
	ConcurrencyRenewBefore time.Duration
	// ConcurrencyAuxiliaryLeasePolicy is inherit (default) or acquire_own (10.10).
	ConcurrencyAuxiliaryLeasePolicy string
	// MeteringRecorder is the optional Phase 3 metering journal port. Nil means
	// checkpoints are retained in-request only (no durable append until Phase 5).
	MeteringRecorder metering.Recorder
	// RequestCoordinator admits customer/logical-request authority once per request (Phase 6).
	RequestCoordinator *authoritycoord.RequestCoordinator
	// AttemptCoordinator admits operator/attempt authority per B-leg (Phase 6).
	AttemptCoordinator *authoritycoord.AttemptCoordinator
	// SnapshotGeneration is the atomic policy/rating generation publisher (Phase 9.3).
	// Admit binds Current() usage/concurrency/rating refs when present.
	SnapshotGeneration *snapshotgen.Publisher
	// TerminalWork accepts durable settle/release intents on post-output failures
	// (Phase 4.5; requirements 7.7, 8.3; design D9).
	TerminalWork *terminalworkapp.IntentService
}

// UsageAuthorityService is the runtime-owned boundary for accounting authority
// admission, settlement, release, advisory usage application, and bounded query access.
type UsageAuthorityService interface {
	Admit(ctx context.Context, in authorityapp.AdmissionInput) (authorityapp.AdmissionResult, error)
	Settle(ctx context.Context, in authorityapp.SettleInput) (authorityapp.SettleResult, error)
	Release(ctx context.Context, in authorityapp.ReleaseInput) (authorityapp.ReleaseResult, error)
	ApplyUsage(ctx context.Context, cmd authorityapp.ApplyUsageCommand) (authorityapp.ApplyUsageResult, error)
}

// ObservabilityRuntime carries structured logging, metrics, and diagnostics toggles.
type ObservabilityRuntime struct {
	Log                        *slog.Logger
	Metrics                    MetricsSink
	ExtensionMetrics           extensions.StageMetrics
	SecretGuardDecisionMetrics extensions.SecretGuardDecisionMetrics
	RouteTrace                 *diag.RouteTraceBuffer
	PolicyDiagnosticsEnabled   bool
	CompletionBufferLimits     completion.BufferLimits
	// ConversationViewObserver is optional narrow diagnostics for bounded conversation-view
	// projection/anchor/steering metrics. Nil is no-op. Labels are bounded enums only (placement, operation, policy, stage).
	ConversationViewObserver ConversationViewObserver
}

// ExtensionRuntime carries the hook bus and frozen per-build extension snapshot.
type ExtensionRuntime struct {
	Bus             *hooks.Bus
	RuntimeSnapshot *extensions.RequestRuntimeSnapshot

	// TerminalPolicyReader resolves session-scoped terminal decision policy overrides
	// at request admission (Task 7.2).
	TerminalPolicyReader TerminalPolicyReader

	// ToolCallFinalizationMaxArgsBytes is the assembler buffer cap from merged
	// feature bundles (0 means default at assembler construction).
	ToolCallFinalizationMaxArgsBytes int

	toolCallFinalizers []toolcall.Finalizer
}

// InterleavedRuntime carries the interleaved-thinking consumer processor port.
type InterleavedRuntime struct {
	Processor InterleavedProcessor
}

// CompactionRuntime carries the process-owned compaction detector reference.
// The detector is shared across generations and never owned by the executor;
// nil is safe and disables compaction observation entirely.
// Detection is observational only: it never alters routing, prompts,
// responses, retries, accounting, or client framing.
type CompactionRuntime struct {
	Detector CompactionDetector
	// BackgroundAux is the generation-bound process scheduler client. The
	// scheduler itself remains process-owned; this interface lets callbacks
	// submit work against the executor's frozen generation binding.
	BackgroundAux auxiliary.BackgroundClient
}

// ExecutorConfig groups executor dependencies for explicit construction at the
// composition root and in tests. Use [NewExecutor] to obtain a runnable executor.
type ExecutorConfig struct {
	Core          CoreRuntime
	Billing       BillingRuntime
	Routing       RoutingRuntime
	Security      SecurityRuntime
	Accounting    AccountingRuntime
	Observability ObservabilityRuntime
	Extension     ExtensionRuntime
	Interleaved   InterleavedRuntime
	Compaction    CompactionRuntime
}

// NewExecutor constructs an [Executor] from grouped runtime configuration.
func NewExecutor(cfg ExecutorConfig) *Executor {
	if cfg.Routing.ExecutionCompositionPolicy == "" {
		cfg.Routing.ExecutionCompositionPolicy = config.ExecutionCompositionSafe
	}
	if cfg.Routing.BackendExecutionResolver == nil && len(cfg.Core.Backends) > 0 {
		m := make(map[string]lipsdk.BackendExecutionClass, len(cfg.Core.Backends))
		for k := range cfg.Core.Backends {
			m[k] = lipsdk.BackendExecutionUnknown
		}
		cfg.Routing.BackendExecutionResolver = routing.BackendExecutionResolverFunc(func(id string) (lipsdk.BackendExecutionClass, bool) {
			c, ok := m[id]
			return c, ok
		})
	}
	return &Executor{
		CoreRuntime:          cfg.Core,
		BillingRuntime:       cfg.Billing,
		RoutingRuntime:       cfg.Routing,
		SecurityRuntime:      cfg.Security,
		AccountingRuntime:    cfg.Accounting,
		ObservabilityRuntime: cfg.Observability,
		ExtensionRuntime:     cfg.Extension,
		InterleavedRuntime:   cfg.Interleaved,
		CompactionRuntime:    cfg.Compaction,
	}
}
