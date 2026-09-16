package runtimebundle

import (
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	runtimecore "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/featurehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

// ProductionOptions carries enterprise/production injection seams (reqs 12.1, 12.3, 12.4).
// Canonical host construction accepts descriptor-bound registrations only.
type ProductionOptions struct {
	BillingTerminalUsageSink billing.TerminalUsageSink
	// BillingStore is the authoritative durable billing boundary used by runtime
	// and read-side report composition. It is intentionally a domain port.
	BillingStore       billing.AuthoritativeBilling
	BillingReports     billing.ReportingStore
	BillingReportsPath string
	BillingIdentity    runtimecore.BillingIdentity
	// BillingCallRatingResolver resolves immutable call/exposure snapshots and never consults authorization holds.
	BillingCallRatingResolver   billing.CallRatingResolver
	BillingProviderCostResolver billing.ProviderCostResolver
	// BillingEconomicRevisionRater drives pure revision valuation from the
	// durable evidence queue. It is intentionally separate from settlement and
	// provider-cost stores; nil keeps the optional worker disabled.
	BillingEconomicRevisionRater billing.PostUsageRater
	// BillingObservationEconomicWorkBuilder converts durable metering
	// observations into immutable customer/provider queue markers. It performs
	// no rating or money mutation; the process-owned relay invokes it after the
	// observation transaction commits.
	BillingObservationEconomicWorkBuilder billing.ObservationEconomicWorkBuilder
	BillingEconomicRevisionReconciler     billing.EconomicRevisionReconciler
	BillingCostPassThroughSettlementStore billing.CostPassThroughSettlementStore
	BillingCustomerUnitLedger             billing.CustomerUnitLedger
	// MaintenanceAccounting receives provider-authoritative maintenance usage on
	// behalf of background maintenance operations (such as prompt cache keepwarm).
	MaintenanceAccounting    billing.ProviderMaintenanceUsageObserver
	BillingPostTurnBatchSize int
	// BillingCreditGate is the required pre-route settled-credit screen for
	// authoritative billing. It is intentionally separate from detailed post-route
	// exposure admission.
	BillingCreditGate runtimecore.BillingCreditGate
	// BillingExposureAdmission is the authoritative post-route operational
	// exposure seam, normally constructed by ComposeBilling from BillingStore.
	BillingExposureAdmission runtimecore.BillingExposureAdmission
	MeteringRecorder         metering.Recorder
	// MeteringAccountWindowStore is the optional nonfinancial gauge query port
	// used by an explicitly configured provider quota policy. It is separate
	// from all billing and customer-unit seams.
	MeteringAccountWindowStore coremetering.AccountWindowStore
	RequestRegistrations       []authority.RequestRegistration
	AttemptRegistrations       []authority.AttemptRegistration
	ConcurrencyRegistration    *authority.ConcurrencyRegistration
	UsageSnapshotSource        economics.RuleSnapshotSource
	ConcurrencySnapshotSource  economics.RuleSnapshotSource
	RatingSnapshotSource       economics.RatingSnapshotSource
	EvidenceSink               authority.EvidenceSink
	MeteringQuerier            metering.Querier
	TrafficObservers           []traffic.Observer
	UsageObservers             []usage.Observer
	PolicyObservers            []policydecision.Observer
	// Terminal-work processor ownership (tasks 4.4–4.5). When TerminalWorkStore is
	// set, Build constructs processor/registry/intents, starts the processor, and
	// injects IntentService into the executor.
	//
	// EffectProviders are composed as: RequestRegistrations -> AuthorityRequestEffectProvider
	// adapters (by descriptor ID), then TerminalWorkProviders merged by ProviderID with
	// explicit entries overriding derived adapters for the same ID.
	TerminalWorkStore          terminalworkapp.RecoveryStore
	TerminalWorkProviders      []terminalworkapp.EffectProvider
	TerminalWorkOwnerID        string
	TerminalWorkClaimTTL       time.Duration
	TerminalWorkClaimLimit     int
	TerminalWorkGlobalMax      int
	TerminalWorkPerProviderMax int
	TerminalWorkTickInterval   time.Duration
	TerminalWorkRenewInterval  time.Duration
	// FeatureHostRegistrations carries startup-only host-feature bindings
	// (Task 8.3/8.4, Requirements 9.3-9.6).
	FeatureHostRegistrations []featurehost.Registration
}

// HasAuthorityOverrides reports whether production authority providers are set.
func (p ProductionOptions) HasAuthorityOverrides() bool {
	return len(p.RequestRegistrations) > 0 || len(p.AttemptRegistrations) > 0 ||
		p.ConcurrencyRegistration != nil
}
