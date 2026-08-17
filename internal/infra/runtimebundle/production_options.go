package runtimebundle

import (
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	runtimecore "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
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
	BillingStore         billing.AuthoritativeBilling
	BillingReports       billing.ReportingStore
	BillingAuthoritative bool
	BillingReportsPath   string
	BillingIdentity      runtimecore.BillingIdentity
	// BillingCallRatingResolver resolves immutable call/exposure snapshots for
	// post-usage customer settlement and never consults authorization holds.
	BillingCallRatingResolver   billing.CallRatingResolver
	BillingProviderCostResolver billing.ProviderCostResolver
	BillingPostTurnBatchSize    int
	BillingPostTurnInterval     time.Duration

	// BillingCreditGate is the required pre-route settled-credit screen for
	// authoritative billing. It is intentionally separate from detailed post-route
	// exposure admission.
	BillingCreditGate runtimecore.BillingCreditGate
	// BillingExposureAdmission is the authoritative post-route operational
	// exposure seam, normally constructed by ComposeBilling from BillingStore.
	BillingExposureAdmission  runtimecore.BillingExposureAdmission
	MeteringRecorder          metering.Recorder
	RequestRegistrations      []authority.RequestRegistration
	AttemptRegistrations      []authority.AttemptRegistration
	ConcurrencyRegistration   *authority.ConcurrencyRegistration
	UsageSnapshotSource       economics.RuleSnapshotSource
	ConcurrencySnapshotSource economics.RuleSnapshotSource
	RatingSnapshotSource      economics.RatingSnapshotSource
	EvidenceSink              authority.EvidenceSink
	MeteringQuerier           metering.Querier
	TrafficObservers          []traffic.Observer
	UsageObservers            []usage.Observer
	PolicyObservers           []policydecision.Observer

	// Terminal-work processor ownership (tasks 4.4–4.5). When TerminalWorkStore is
	// set, Build constructs processor/registry/intents, starts the processor, and
	// injects IntentService into the executor.
	//
	// EffectProviders are composed as: RequestRegistrations → AuthorityRequestEffectProvider
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
}

// HasAuthorityOverrides reports whether production authority providers are set.
func (p ProductionOptions) HasAuthorityOverrides() bool {
	return len(p.RequestRegistrations) > 0 || len(p.AttemptRegistrations) > 0 ||
		p.ConcurrencyRegistration != nil
}
