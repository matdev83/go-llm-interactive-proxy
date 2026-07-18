package runtimebundle

import (
	"time"

	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

// ProductionOptions carries enterprise/production injection seams (reqs 12.1, 12.3, 12.4).
// Prefer descriptor-bound registrations; deprecated parallel slices are rejected unless
// already normalized by pkg/lipruntime.
type ProductionOptions struct {
	MeteringRecorder          metering.Recorder
	RequestRegistrations      []authority.RequestRegistration
	AttemptRegistrations      []authority.AttemptRegistration
	ConcurrencyRegistration   *authority.ConcurrencyRegistration
	RaterRegistrations        []economics.RaterRegistration
	RequestProviders          []authority.RequestProvider   // Deprecated: use RequestRegistrations.
	AttemptProviders          []authority.AttemptProvider   // Deprecated: use AttemptRegistrations.
	ConcurrencyProvider       authority.ConcurrencyProvider // Deprecated: use ConcurrencyRegistration.
	UsageSnapshotSource       economics.RuleSnapshotSource
	ConcurrencySnapshotSource economics.RuleSnapshotSource
	RatingSnapshotSource      economics.RatingSnapshotSource
	Rater                     economics.Rater // Deprecated: use RaterRegistrations.
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
		p.ConcurrencyRegistration != nil || len(p.RequestProviders) > 0 ||
		len(p.AttemptProviders) > 0 || p.ConcurrencyProvider != nil
}
