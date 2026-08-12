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
// Provider injection uses descriptor-bound registrations only:
// RequestRegistrations, AttemptRegistrations, and ConcurrencyRegistration.
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
	AttemptRegistrations    []authority.AttemptRegistration // ConcurrencyRegistration binds descriptor and concurrency authority.
	ConcurrencyRegistration *authority.ConcurrencyRegistration

	// UsageSnapshotSource supplies the usage-authority source-fetch metadata
	// view for publication and RefreshSnapshots. It is not enforcement evidence;
	// executable generations bind descriptor-bound registrations instead.
	UsageSnapshotSource economics.RuleSnapshotSource
	// ConcurrencySnapshotSource supplies the concurrency source-fetch metadata
	// view for publication and RefreshSnapshots.
	ConcurrencySnapshotSource economics.RuleSnapshotSource
	// RatingSnapshotSource supplies the rating/catalog source-fetch metadata
	// view for publication and RefreshSnapshots.
	RatingSnapshotSource economics.RatingSnapshotSource
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
}
