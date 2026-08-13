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
	// BillingTerminalHandoff is the optional durable post-terminal TUR appender.
	BillingTerminalHandoff billing.UsageRecordAppender
	// BillingStore is the authoritative durable billing boundary used by runtime
	// and read-side report composition. It is intentionally a domain port.
	BillingStore         billing.AuthoritativeBilling
	BillingReports       billing.ReportingStore
	BillingAuthoritative bool
	BillingReportsPath   string
	// BillingHoldTTL is the admission hold lifetime from ComposeBilling.
	// Zero lets BuildHost apply accounting.billing.hold_ttl (default 15m).
	BillingHoldTTL  time.Duration
	BillingIdentity runtimecore.BillingIdentity
	// BillingRatingResolver resolves the immutable snapshots required by the
	// post-turn worker. It is mandatory when authoritative billing is enabled.
	BillingRatingResolver    billing.RatingResolver
	BillingPostTurnBatchSize int
	BillingPostTurnInterval  time.Duration

	// BillingAdmission is the production composition injection for the durable
	// billing adapter (see internal/infra/billingadmission.NewAdapter). It is
	// attached to the runtime's sole pre-provider authorization seam. Nil is
	// allowed only when BillingAdmissionRequired is false, preserving
	// deployments that have not enabled billing.
	BillingAdmission          runtimecore.BillingAdmission
	BillingAdmissionRequired  bool
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
