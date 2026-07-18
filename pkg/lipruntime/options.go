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
//
// Prefer descriptor-bound registrations (RequestRegistrations,
// AttemptRegistrations, ConcurrencyRegistration, RaterRegistrations). Legacy
// parallel slices remain for deterministic migration only; see field docs.
type Options struct {
	// ConfigPath is the YAML runtime config path (required).
	ConfigPath string
	// LogWriter receives bootstrap logger output; nil means discard for library use.
	LogWriter io.Writer

	// MeteringRecorder overrides the config-built metering journal recorder.
	MeteringRecorder metering.Recorder

	// RequestRegistrations bind descriptor, priority, and request authority.
	RequestRegistrations []authority.RequestRegistration
	// AttemptRegistrations bind descriptor, priority, and attempt authority.
	AttemptRegistrations []authority.AttemptRegistration
	// ConcurrencyRegistration binds descriptor and concurrency authority.
	ConcurrencyRegistration *authority.ConcurrencyRegistration
	// RaterRegistrations bind rater identity, perspective, and rater instance.
	RaterRegistrations []economics.RaterRegistration

	// RequestProviders is deprecated. Use RequestRegistrations. Accepted only
	// with matching request-stage ProviderDescriptors (exact cardinality,
	// index-paired); never invents production-request-%d identities.
	RequestProviders []authority.RequestProvider
	// AttemptProviders is deprecated. Use AttemptRegistrations. Accepted only
	// with matching attempt-stage ProviderDescriptors (exact cardinality,
	// index-paired); never invents production-attempt-%d identities.
	AttemptProviders []authority.AttemptProvider
	// ConcurrencyProvider is deprecated. Use ConcurrencyRegistration. Accepted
	// only with exactly one lease-stage ProviderDescriptor when the registration
	// pointer is nil.
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
	// Rater is deprecated. Use RaterRegistrations. When set alone, it maps to a
	// deterministic operator registration with ID "legacy-production-rater".
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
	// ProviderDescriptors is deprecated for authority binding. Prefer embedding
	// descriptors on registrations. Legacy authority slices require matching
	// stage descriptors here; observer-only descriptors remain validated at Build.
	ProviderDescriptors []authority.ProviderDescriptor
}
