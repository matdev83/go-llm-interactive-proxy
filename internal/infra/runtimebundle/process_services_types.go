package runtimebundle

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	concurrencyapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
	ssessionapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	accountingledger "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/ledger"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/processhost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/trust"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	lipstate "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
)

// ProcessTracing holds process-owned tracing shutdown and outbound-propagation state.
// Constructed once at process startup (typically via tracing.Init in bootstrap).
type ProcessTracing struct {
	Shutdown func(context.Context) error
	Active   bool
}

// ProcessServices owns process-scoped resources constructed once per process.
// Generation compilation receives a non-owning reference and must not Close it.
type ProcessServices struct {
	Logger                *slog.Logger
	FactoryCatalog        *pluginreg.Registry
	Tracing               ProcessTracing
	Metrics               *metrics.Bundle
	DatabasePools         *db.PoolRegistry
	Continuity            b2bua.Store
	SecureSessions        ssessionapp.Store
	DecodeAdmission       lipsdk.DecodeAdmission
	ALegLifecycle         *leglifecycle.Coordinator
	AffinityStore         affinity.Store
	CandidateHealth       policy.CandidateHealth
	ExtensionState        lipstate.Store
	AccountingLedger      accountingledger.Recorder
	MeteringRecorder      metering.Recorder
	UsageAuthority        *authorityapp.Service
	Concurrency           *concurrencyapp.Service
	SnapshotGeneration    *snapshotgen.Publisher
	SnapshotController    *SnapshotController
	MeteringQuerier       metering.Querier
	TerminalWorkProcessor *terminalworkapp.Processor
	TerminalWorkRegistry  *terminalworkapp.Registry
	TerminalWorkQueries   *terminalworkapp.QueryService
	TerminalWorkMetrics   *terminalworkapp.MetricsObserver

	// Internal handles required by candidate compilation (non-API).
	persistence       *persistenceRuntime
	controlPlane      *controlPlaneRuntime
	usageRT           *usageAuthorityRuntime
	concurrencyRT     *concurrencyAuthorityRuntime
	terminalWorkRT    *terminalWorkRuntime
	dualPlaneMigrator *dualPlaneMigrator
	policyObs         policydecision.Observer
	sharedMutable     *sharedMutableRuntime
	accountingStores  *processAccountingStores
	meteringRT        *meteringRuntime
	cfg               *config.Config
	opts              *BuildOptions

	closers   []func() error
	closeOnce sync.Once
	closeErr  error
	closed    atomic.Bool
}

// ProcessServicesInput configures [NewProcessServices].
type ProcessServicesInput struct {
	Cfg     *config.Config
	Log     *slog.Logger
	Opts    *BuildOptions
	Tracing ProcessTracing
	// PluginHost, PluginArtifacts, and PluginStagingDir are process-owned
	// discovered-plugin resources. When set, NewProcessServices takes sole
	// ownership and disposes them once after generation retirement in reverse
	// acquisition order (host → artifacts → staging), so Windows releases the
	// staged executable handles (VerifiedArtifact.Close) before staging removal.
	PluginHost       *processhost.Host
	PluginArtifacts  []*trust.VerifiedArtifact
	PluginStagingDir string
}
