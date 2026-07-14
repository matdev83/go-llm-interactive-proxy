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
	// UsageSnapshotSource supplies the usage-authority snapshot for publication.
	UsageSnapshotSource economics.RuleSnapshotSource
	// ConcurrencySnapshotSource supplies the concurrency snapshot for publication.
	ConcurrencySnapshotSource economics.RuleSnapshotSource
	// RatingSnapshotSource supplies the rating/catalog snapshot for publication.
	RatingSnapshotSource economics.RatingSnapshotSource
	// Rater is reserved for enterprise rating injection; rating snapshot source
	// is the Phase 10 publication seam (full rater wiring may extend later).
	Rater economics.Rater
	// TrafficObservers are merged with feature-bundle traffic observers.
	TrafficObservers []traffic.Observer
	// UsageObservers are merged with feature-bundle usage observers.
	UsageObservers []usage.Observer
	// PolicyObservers are chained into policy-decision evidence.
	PolicyObservers []policydecision.Observer
	// ProviderDescriptors declare authority vs observer postures for injected
	// providers. Observers cannot use StrengthRequired (requirement 12.7).
	ProviderDescriptors []authority.ProviderDescriptor
}
