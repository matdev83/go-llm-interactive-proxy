package lipruntime

import (
	"io"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

// Options configures public production composition (requirement 12.3, 12.4).
type Options struct {
	// ConfigPath is the YAML runtime config path (required).
	ConfigPath string
	// LogWriter receives bootstrap logger output; nil means discard for library use.
	LogWriter io.Writer

	// MeteringRecorder overrides the config-built metering journal recorder.
	MeteringRecorder metering.Recorder
	// RequestProviders are injected logical-request authority providers.
	RequestProviders []authority.RequestProvider
	// AttemptProviders are injected attempt authority providers.
	AttemptProviders []authority.AttemptProvider
	// ConcurrencyProvider overrides the config-built concurrency lease provider.
	ConcurrencyProvider authority.ConcurrencyProvider
	// UsageSnapshotSource supplies the usage-authority snapshot for publication
	// and RefreshSnapshots.
	UsageSnapshotSource economics.RuleSnapshotSource
	// ConcurrencySnapshotSource supplies the concurrency snapshot for publication
	// and RefreshSnapshots.
	ConcurrencySnapshotSource economics.RuleSnapshotSource
	// RatingSnapshotSource supplies the rating/catalog snapshot for publication
	// and RefreshSnapshots.
	RatingSnapshotSource economics.RatingSnapshotSource
	// Rater is the enterprise rating provider injected onto the accounting runtime.
	Rater economics.Rater
	// EvidenceSink projects authority decisions into policy and control-plane evidence.
	EvidenceSink authority.EvidenceSink
	// MeteringQuerier is the enterprise metering query mount (requirement 12.1).
	// Bounded control-plane query filter expansion remains Phase 11.
	MeteringQuerier metering.Querier
	// TrafficObservers are merged with feature-bundle traffic observers.
	TrafficObservers []traffic.Observer
	// UsageObservers are merged with feature-bundle usage observers.
	UsageObservers []usage.Observer
	// PolicyObservers are chained into policy-decision evidence (additive to EvidenceSink).
	PolicyObservers []policydecision.Observer
	// ProviderDescriptors declare authority vs observer postures for injected
	// providers. Observers cannot use StrengthRequired (requirement 12.7).
	ProviderDescriptors []authority.ProviderDescriptor
}
