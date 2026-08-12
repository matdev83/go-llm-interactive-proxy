package runtime

import (
	"context"
	"log/slog"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

// BillingIdentity is the composition-root identity bundle shared by billing
// admission and terminal TUR seal so those seams cannot drift.
type BillingIdentity struct {
	AccountID          func(context.Context, lipapi.Call) string
	AuthorizationID    func(context.Context, lipapi.Call, string) string
	CustomerPricingRef func(context.Context, lipapi.Call) billing.VersionRef
	ChargePolicyRef    func(context.Context, lipapi.Call) billing.VersionRef
	OperatorRateRef    func(context.Context, string, string) billing.VersionRef
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
}

// BillingRuntime is the only executor grouping for monetary admission, TUR
// handoff, unused-hold release, and observational LUR listeners. Token quota
// and usage-authority stay on AccountingRuntime.
type BillingRuntime struct {
	// BillingAdmission runs after side-effect-free route planning and before
	// provider/connector work. It may compute a max customer charge and
	// atomically create a durable authorization hold. Streaming and post-turn
	// handlers never call it.
	BillingAdmission BillingAdmission
	// BillingLegObserver receives one recorded LUR at terminal ownership.
	// It is observational only and never participates in authorization,
	// settlement, retry, cancellation, or client-visible output.
	BillingLegObserver BillingLegObserver
	// BillingTerminalHandoff appends one sealed TUR after request terminal
	// ownership. It is never called during stream processing and its failure
	// cannot change selected output.
	BillingTerminalHandoff billing.UsageRecordAppender
	// BillingHoldReleaser releases unused authorization holds when TUR handoff
	// exhausts without evidence. Composition wires the same durable store used
	// for admission; runtime never duck-types this port.
	BillingHoldReleaser billing.HoldReleaser
	// BillingIdentity is the single composition identity bundle for admission
	// stamping. Terminal TUR seal reads the stamped values and does not
	// re-resolve these functions.
	BillingIdentity BillingIdentity
	// BillingAuthoritative is the composition cutover flag. When true, Bun TUR
	// handoff, post-turn settlement, and journal-backed reports are the sole
	// monetary authority. Stream handlers never enrich prices or write the
	// legacy token ledger regardless of this flag; non-money quota/rate-limit
	// coordination remains on the existing authority ports.
	BillingAuthoritative bool
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
}

// ExtensionRuntime carries the hook bus and frozen per-build extension snapshot.
type ExtensionRuntime struct {
	Bus             *hooks.Bus
	RuntimeSnapshot *extensions.RequestRuntimeSnapshot

	// ToolCallFinalizationMaxArgsBytes is the assembler buffer cap from merged
	// feature bundles (0 means default at assembler construction).
	ToolCallFinalizationMaxArgsBytes int

	toolCallFinalizers []toolcall.Finalizer
}

// InterleavedRuntime carries interleaved-thinking shaping configuration and memo storage.
type InterleavedRuntime struct {
	InterleavedConfig interleavedthinking.ShapeConfig
	MemoStore         interleavedthinking.MemoStore
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
}

// NewExecutor constructs an [Executor] from grouped runtime configuration.
func NewExecutor(cfg ExecutorConfig) *Executor {
	return &Executor{
		CoreRuntime:          cfg.Core,
		BillingRuntime:       cfg.Billing,
		RoutingRuntime:       cfg.Routing,
		SecurityRuntime:      cfg.Security,
		AccountingRuntime:    cfg.Accounting,
		ObservabilityRuntime: cfg.Observability,
		ExtensionRuntime:     cfg.Extension,
		InterleavedRuntime:   cfg.Interleaved,
	}
}
