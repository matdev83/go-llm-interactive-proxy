package runtimebundle

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

// ProductionOptions carries first-class enterprise/production injection seams
// (requirements 12.1, 12.3, 12.4). These must not live only under TestingOptions.
type ProductionOptions struct {
	// MeteringRecorder, when non-nil, replaces the config-built metering journal
	// recorder on the executor.
	MeteringRecorder metering.Recorder
	// RequestProviders are additional/replacement logical-request authority slots
	// evaluated by the request coordinator (public contracts only).
	RequestProviders []authority.RequestProvider
	// AttemptProviders are additional/replacement attempt authority slots.
	AttemptProviders []authority.AttemptProvider
	// ConcurrencyProvider, when non-nil, replaces the config-built concurrency
	// lease provider on the executor.
	ConcurrencyProvider authority.ConcurrencyProvider
	// UsageSnapshotSource, when non-nil, supplies the usage-authority snapshot
	// identity published into the initial RuntimeGeneration.
	UsageSnapshotSource economics.RuleSnapshotSource
	// ConcurrencySnapshotSource, when non-nil, supplies the concurrency snapshot
	// identity for the initial RuntimeGeneration.
	ConcurrencySnapshotSource economics.RuleSnapshotSource
	// RatingSnapshotSource, when non-nil, supplies the rating/catalog snapshot
	// for the initial RuntimeGeneration.
	RatingSnapshotSource economics.RatingSnapshotSource
	// TrafficObservers are merged into the runtime extension surface.
	TrafficObservers []traffic.Observer
	// UsageObservers are merged into the runtime extension surface.
	UsageObservers []usage.Observer
	// PolicyObservers are chained as policy-decision evidence observers.
	PolicyObservers []policydecision.Observer
}

// HasAuthorityOverrides reports whether production authority providers are set.
func (p ProductionOptions) HasAuthorityOverrides() bool {
	return len(p.RequestProviders) > 0 || len(p.AttemptProviders) > 0 || p.ConcurrencyProvider != nil
}
