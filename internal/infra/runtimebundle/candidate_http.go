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

func CompileCandidate(ctx context.Context, in GenerationCompileInput) (*CandidateHTTPCompile, error) {
	assem, err := compileCandidate(ctx, in)
	if err != nil {
		return nil, err
	}
	return &CandidateHTTPCompile{assem: assem}, nil
}

func (c *CandidateHTTPCompile) a() *candidateAssembly {
	if c == nil {
		return nil
	}
	return c.assem
}

func (c *CandidateHTTPCompile) Close() error {
	if a := c.a(); a != nil {
		return a.Close()
	}
	return nil
}

func (c *CandidateHTTPCompile) Quiesce(ctx context.Context) error {
	if a := c.a(); a != nil {
		return a.Quiesce(ctx)
	}
	return nil
}

func (c *CandidateHTTPCompile) RollbackUnpublished() error {
	if a := c.a(); a != nil {
		return a.RollbackUnpublished()
	}
	return nil
}

func (c *CandidateHTTPCompile) Executor() *runtime.Executor {
	if a := c.a(); a != nil {
		return a.execution.executor
	}
	return nil
}

func (c *CandidateHTTPCompile) DecodeAdmission() lipsdk.DecodeAdmission {
	if a := c.a(); a != nil {
		return a.execution.decodeAdmission
	}
	return nil
}

func (c *CandidateHTTPCompile) EffectiveDefaultRoute() string {
	if a := c.a(); a != nil {
		return a.execution.effectiveDefaultRoute
	}
	return ""
}

func (c *CandidateHTTPCompile) RoutePrefixes() []string {
	if a := c.a(); a != nil {
		return append([]string(nil), a.execution.routePrefixes...)
	}
	return nil
}

func (c *CandidateHTTPCompile) ModelRegistry() *modelregistry.Registry {
	if a := c.a(); a != nil {
		return a.models.registry
	}
	return nil
}

func (c *CandidateHTTPCompile) PluginRegistry() *pluginreg.Registry {
	if a := c.a(); a != nil {
		return a.process.pluginRegistry
	}
	return nil
}

func (c *CandidateHTTPCompile) RuntimeSnapshot() *extensions.RequestRuntimeSnapshot {
	if a := c.a(); a != nil {
		return a.security.runtimeSnapshot
	}
	return nil
}

func (c *CandidateHTTPCompile) Metrics() *metrics.Bundle {
	if a := c.a(); a != nil {
		return a.process.metrics
	}
	return nil
}

func (c *CandidateHTTPCompile) Store() b2bua.Store {
	if a := c.a(); a != nil {
		return a.process.store
	}
	return nil
}

func (c *CandidateHTTPCompile) StandardHTTPInput(frozen *config.Config, regs []lipsdk.Registration, route string) httpcontract.StandardHTTPInput {
	if a := c.a(); a != nil {
		return buildStandardHTTPInput(context.TODO(), a, frozen, regs, route)
	}
	return httpcontract.StandardHTTPInput{}
}
