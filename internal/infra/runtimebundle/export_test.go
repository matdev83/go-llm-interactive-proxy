package runtimebundle

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	concurrencyapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	ssessionapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

func NewGenerationBundleForTest(models *modelregistry.Runtime, catalog *modelcatalog.CatalogRuntime) *GenerationBundle {
	return &GenerationBundle{
		models: generationModelViews{
			models:  models,
			catalog: catalog,
		},
	}
}

func NewGenerationBundleWithLedgerForTest(ledger *ResourceLedger) *GenerationBundle {
	return &GenerationBundle{ledger: ledger}
}

func NewGenerationBundleWithPublicationForTest(auth []httpauth.Provider, regs []lipsdk.Registration) *GenerationBundle {
	return newGenerationBundle(generationBundleInput{
		httpAuth:      auth,
		registrations: regs,
	})
}

func TransferLedgerOwnershipForTest(c *CandidateHTTPCompile) *ResourceLedger {
	if c == nil || c.assem == nil {
		return nil
	}
	return c.assem.transferLedgerOwnership()
}

func NewCandidateAssemblyForTest(ledger *ResourceLedger) *candidateAssembly {
	return &candidateAssembly{ledger: ledger}
}

// NewCandidateRuntimeForTest constructs an opaque candidate handle around a ledger.
func NewCandidateRuntimeForTest(ledger *ResourceLedger) *CandidateHTTPCompile {
	return &CandidateHTTPCompile{assem: &candidateAssembly{ledger: ledger}}
}

func CandidateAssemblyOf(c *CandidateHTTPCompile) *candidateAssembly {
	if c == nil {
		return nil
	}
	return c.assem
}

// CompileCandidateAssembly exposes compileCandidate to runtimebundle_test.
func CompileCandidateAssembly(ctx context.Context, in GenerationCompileInput) (*candidateAssembly, error) {
	return compileCandidate(ctx, in)
}

func HostManager(h *Host) *runtimehost.Manager {
	if h == nil {
		return nil
	}
	return h.manager
}

func HostProcess(h *Host) *ProcessServices {
	if h == nil {
		return nil
	}
	return h.process
}

func HostCoordinator(h *Host) *runtimehost.Coordinator {
	if h == nil {
		return nil
	}
	return h.coordinator
}

func HostGenerationExecutor(h *Host) *runtimehost.GenerationExecutor {
	if h == nil {
		return nil
	}
	return h.executor
}

func HostShutdownTracing(h *Host) func(context.Context) error {
	if h == nil {
		return nil
	}
	return h.shutdownTracing
}

func SetHostShutdownTracing(h *Host, fn func(context.Context) error) {
	if h == nil {
		return
	}
	h.shutdownTracing = fn
}

func HostFixedStreamRecovery(h *Host) config.StreamRecoveryOverrides {
	if h == nil {
		return config.StreamRecoveryOverrides{}
	}
	return h.fixedStreamRecovery
}

// HostCloseTestInput constructs a Host for close-ordering unit tests.
type HostCloseTestInput struct {
	Coordinator     *runtimehost.Coordinator
	Manager         *runtimehost.Manager
	Process         *ProcessServices
	Executor        *runtimehost.GenerationExecutor
	ShutdownTracing func(context.Context) error
	Dispatcher      *runtimehost.GenerationDispatcher
	Logger          *slog.Logger
	Config          *config.Config
	Effective       *config.EffectiveConfig
}

func NewHostForCloseTest(in HostCloseTestInput) *Host {
	return &Host{
		coordinator:     in.Coordinator,
		manager:         in.Manager,
		process:         in.Process,
		executor:        in.Executor,
		shutdownTracing: in.ShutdownTracing,
		dispatcher:      in.Dispatcher,
		logger:          in.Logger,
		config:          in.Config,
		effective:       in.Effective,
	}
}

// Test-only candidate field accessors (PR B2: no production getter wall).
func CandidateHTTPAuthProviders(c *CandidateHTTPCompile) []httpauth.Provider {
	if a := CandidateAssemblyOf(c); a != nil {
		return append([]httpauth.Provider(nil), a.security.httpAuth...)
	}
	return nil
}
func CandidateAuthEventDispatcher(c *CandidateHTTPCompile) *auth.EventDispatcher {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.security.authEvents
	}
	return nil
}
func CandidateUsageAuthority(c *CandidateHTTPCompile) *authorityapp.Service {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.usageAuthority
	}
	return nil
}
func CandidateReadinessReport(c *CandidateHTTPCompile) *controlplane.ReadinessReportService {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.operations.readinessReport
	}
	return nil
}
func CandidateCatalogRuntime(c *CandidateHTTPCompile) *modelcatalog.CatalogRuntime {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.models.catalog
	}
	return nil
}
func CandidateModelRegistryRuntime(c *CandidateHTTPCompile) *modelregistry.Runtime {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.models.registryRuntime
	}
	return nil
}
func CandidateSecureSessionStore(c *CandidateHTTPCompile) ssessionapp.Store {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.security.secureSessionStore
	}
	return nil
}
func CandidateConcurrencyAuthority(c *CandidateHTTPCompile) *concurrencyapp.Service {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.concurrencyAuthority
	}
	return nil
}
func CandidateControlPlaneQueries(c *CandidateHTTPCompile) *controlplane.QueryService {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.controlPlaneQueries
	}
	return nil
}
func CandidateControlPlaneStatus(c *CandidateHTTPCompile) *controlplane.Status {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.controlPlaneStatus
	}
	return nil
}
func CandidateControlPlaneRetention(c *CandidateHTTPCompile) *controlplane.RetentionController {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.controlPlaneRetention
	}
	return nil
}
func CandidateSnapshotGeneration(c *CandidateHTTPCompile) *snapshotgen.Publisher {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.snapshotGeneration
	}
	return nil
}
func CandidateSnapshotController(c *CandidateHTTPCompile) *SnapshotController {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.snapshotController
	}
	return nil
}
func CandidateDatabasePools(c *CandidateHTTPCompile) *db.PoolRegistry {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.databasePools
	}
	return nil
}
func CandidateMeteringQuerier(c *CandidateHTTPCompile) metering.Querier {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.process.meteringQuerier
	}
	return nil
}
func CandidateTokenAccountingAdmin(c *CandidateHTTPCompile) *accountingapp.Service {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.operations.tokenAccountingAdmin
	}
	return nil
}
func CandidateSecretGuardInventory(c *CandidateHTTPCompile) *diag.InventoryExtras {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.operations.secretGuardInventory
	}
	return nil
}
func CandidateTerminalWorkProcessor(c *CandidateHTTPCompile) *terminalworkapp.Processor {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.operations.terminalProcessor
	}
	return nil
}
func CandidateTerminalWorkRegistry(c *CandidateHTTPCompile) *terminalworkapp.Registry {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.operations.terminalRegistry
	}
	return nil
}
func CandidateTerminalWorkQueries(c *CandidateHTTPCompile) *terminalworkapp.QueryService {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.operations.terminalQueries
	}
	return nil
}
func CandidateTerminalWorkMetrics(c *CandidateHTTPCompile) *terminalworkapp.MetricsObserver {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.operations.terminalMetrics
	}
	return nil
}
func CandidateUpstreamHTTP(c *CandidateHTTPCompile) *http.Client {
	if a := CandidateAssemblyOf(c); a != nil {
		return a.execution.upstreamHTTP
	}
	return nil
}

// White-box method aliases for package runtimebundle tests (not exported to other packages).
func (c *CandidateHTTPCompile) HTTPAuthProviders() []httpauth.Provider {
	return CandidateHTTPAuthProviders(c)
}
func (c *CandidateHTTPCompile) AuthEventDispatcher() *auth.EventDispatcher {
	return CandidateAuthEventDispatcher(c)
}
func (c *CandidateHTTPCompile) UsageAuthority() *authorityapp.Service {
	return CandidateUsageAuthority(c)
}
func (c *CandidateHTTPCompile) ReadinessReport() *controlplane.ReadinessReportService {
	return CandidateReadinessReport(c)
}
func (c *CandidateHTTPCompile) CatalogRuntime() *modelcatalog.CatalogRuntime {
	return CandidateCatalogRuntime(c)
}
func (c *CandidateHTTPCompile) ModelRegistryRuntime() *modelregistry.Runtime {
	return CandidateModelRegistryRuntime(c)
}
func (c *CandidateHTTPCompile) SecureSessionStore() ssessionapp.Store {
	return CandidateSecureSessionStore(c)
}
func (c *CandidateHTTPCompile) ConcurrencyAuthority() *concurrencyapp.Service {
	return CandidateConcurrencyAuthority(c)
}
func (c *CandidateHTTPCompile) ControlPlaneQueries() *controlplane.QueryService {
	return CandidateControlPlaneQueries(c)
}
func (c *CandidateHTTPCompile) ControlPlaneStatus() *controlplane.Status {
	return CandidateControlPlaneStatus(c)
}
func (c *CandidateHTTPCompile) ControlPlaneRetention() *controlplane.RetentionController {
	return CandidateControlPlaneRetention(c)
}
func (c *CandidateHTTPCompile) SnapshotGeneration() *snapshotgen.Publisher {
	return CandidateSnapshotGeneration(c)
}
func (c *CandidateHTTPCompile) SnapshotController() *SnapshotController {
	return CandidateSnapshotController(c)
}
func (c *CandidateHTTPCompile) DatabasePools() *db.PoolRegistry   { return CandidateDatabasePools(c) }
func (c *CandidateHTTPCompile) MeteringQuerier() metering.Querier { return CandidateMeteringQuerier(c) }
func (c *CandidateHTTPCompile) TokenAccountingAdmin() *accountingapp.Service {
	return CandidateTokenAccountingAdmin(c)
}
func (c *CandidateHTTPCompile) SecretGuardInventory() *diag.InventoryExtras {
	return CandidateSecretGuardInventory(c)
}
func (c *CandidateHTTPCompile) TerminalWorkProcessor() *terminalworkapp.Processor {
	return CandidateTerminalWorkProcessor(c)
}
func (c *CandidateHTTPCompile) TerminalWorkRegistry() *terminalworkapp.Registry {
	return CandidateTerminalWorkRegistry(c)
}
func (c *CandidateHTTPCompile) TerminalWorkQueries() *terminalworkapp.QueryService {
	return CandidateTerminalWorkQueries(c)
}
func (c *CandidateHTTPCompile) TerminalWorkMetrics() *terminalworkapp.MetricsObserver {
	return CandidateTerminalWorkMetrics(c)
}
func (c *CandidateHTTPCompile) UpstreamHTTP() *http.Client { return CandidateUpstreamHTTP(c) }
