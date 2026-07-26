package runtimebundle

import (
	"context"
	"net/http"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	concurrencyapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	ssessionapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

type candidateExecutionGroup struct {
	executor              *runtime.Executor
	routePrefixes         []string
	effectiveDefaultRoute string
	decodeAdmission       lipsdk.DecodeAdmission
	upstreamHTTP          *http.Client
}
type candidateSecurityGroup struct {
	httpAuth           []httpauth.Provider
	secureSessionStore ssessionapp.Store
	authEvents         *auth.EventDispatcher
	runtimeSnapshot    *extensions.RequestRuntimeSnapshot
}
type candidateModelGroup struct {
	catalog         *modelcatalog.CatalogRuntime
	registry        *modelregistry.Registry
	registryRuntime *modelregistry.Runtime
}
type candidateOperationsGroup struct {
	tokenAccountingAdmin *accountingapp.Service
	readinessReport      *controlplane.ReadinessReportService
	secretGuardInventory *diag.InventoryExtras
	terminalProcessor    *terminalworkapp.Processor
	terminalRegistry     *terminalworkapp.Registry
	terminalQueries      *terminalworkapp.QueryService
	terminalMetrics      *terminalworkapp.MetricsObserver
}

type candidateProcessRefs struct {
	store                 b2bua.Store
	pluginRegistry        *pluginreg.Registry
	databasePools         *db.PoolRegistry
	metrics               *metrics.Bundle
	controlPlaneQueries   *controlplane.QueryService
	controlPlaneStatus    *controlplane.Status
	controlPlaneRetention *controlplane.RetentionController
	usageAuthority        *authorityapp.Service
	concurrencyAuthority  *concurrencyapp.Service
	snapshotGeneration    *snapshotgen.Publisher
	snapshotController    *SnapshotController
	meteringQuerier       metering.Querier
}

type candidateAssembly struct {
	execution                      candidateExecutionGroup
	security                       candidateSecurityGroup
	models                         candidateModelGroup
	operations                     candidateOperationsGroup
	process                        candidateProcessRefs
	ledger                         *ResourceLedger
	lifeMu                         sync.Mutex
	lifeClaimed, ledgerTransferred bool
	terminalWorkReady              func(context.Context) error
	terminalWorkRT                 *terminalWorkRuntime
}

var _ interface {
	Quiesce(context.Context) error
	Close() error
} = (*candidateAssembly)(nil)

type CandidateHTTPCompile struct{ assem *candidateAssembly }

// CompileCandidate builds a candidate HTTP assembly for one generation compile.
func CompileCandidate(ctx context.Context, in GenerationCompileInput) (*CandidateHTTPCompile, error) {
	assem, err := compileCandidate(ctx, in)
	if err != nil {
		return nil, err
	}
	return &CandidateHTTPCompile{assem: assem}, nil
}

// a returns the underlying assembly when the compile handle is non-nil.
func (c *CandidateHTTPCompile) a() *candidateAssembly {
	if c == nil {
		return nil
	}
	return c.assem
}

// candidateField returns pick(assem) when CandidateHTTPCompile holds an assembly.
func candidateField[T any](c *CandidateHTTPCompile, pick func(*candidateAssembly) T) (zero T) {
	if a := c.a(); a != nil {
		return pick(a)
	}
	return
}

// CandidateHTTPCompile accessors are nil-safe views over candidateAssembly fields.
func (c *CandidateHTTPCompile) Close() error { return candidateField(c, (*candidateAssembly).Close) }

func (c *CandidateHTTPCompile) Quiesce(ctx context.Context) error {
	return candidateField(c, func(a *candidateAssembly) error { return a.Quiesce(ctx) })
}

func (c *CandidateHTTPCompile) RollbackUnpublished() error {
	return candidateField(c, (*candidateAssembly).RollbackUnpublished)
}

func (c *CandidateHTTPCompile) Executor() *runtime.Executor {
	return candidateField(c, func(a *candidateAssembly) *runtime.Executor { return a.execution.executor })
}

func (c *CandidateHTTPCompile) DecodeAdmission() lipsdk.DecodeAdmission {
	return candidateField(c, func(a *candidateAssembly) lipsdk.DecodeAdmission { return a.execution.decodeAdmission })
}

func (c *CandidateHTTPCompile) EffectiveDefaultRoute() string {
	return candidateField(c, func(a *candidateAssembly) string { return a.execution.effectiveDefaultRoute })
}

func (c *CandidateHTTPCompile) RoutePrefixes() []string {
	return candidateField(c, func(a *candidateAssembly) []string { return append([]string(nil), a.execution.routePrefixes...) })
}

func (c *CandidateHTTPCompile) ModelRegistry() *modelregistry.Registry {
	return candidateField(c, func(a *candidateAssembly) *modelregistry.Registry { return a.models.registry })
}

func (c *CandidateHTTPCompile) PluginRegistry() *pluginreg.Registry {
	return candidateField(c, func(a *candidateAssembly) *pluginreg.Registry { return a.process.pluginRegistry })
}

func (c *CandidateHTTPCompile) RuntimeSnapshot() *extensions.RequestRuntimeSnapshot {
	return candidateField(c, func(a *candidateAssembly) *extensions.RequestRuntimeSnapshot { return a.security.runtimeSnapshot })
}

func (c *CandidateHTTPCompile) Metrics() *metrics.Bundle {
	return candidateField(c, func(a *candidateAssembly) *metrics.Bundle { return a.process.metrics })
}

func (c *CandidateHTTPCompile) Store() b2bua.Store {
	return candidateField(c, func(a *candidateAssembly) b2bua.Store { return a.process.store })
}

func (c *CandidateHTTPCompile) Ledger() *ResourceLedger {
	return candidateField(c, func(a *candidateAssembly) *ResourceLedger { return a.ledger })
}

func (c *CandidateHTTPCompile) StandardHTTPInput(frozen *config.Config, regs []lipsdk.Registration, route string) httpcontract.StandardHTTPInput {
	return candidateField(c, func(a *candidateAssembly) httpcontract.StandardHTTPInput {
		return buildStandardHTTPInput(a, frozen, regs, route)
	})
}
