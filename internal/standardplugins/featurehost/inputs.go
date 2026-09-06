package featurehost

import (
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	lipstate "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
)

// ProcessInput contains only generic process capabilities required by the standard feature set.
// It MUST NOT accept *runtimebundle.BuildOptions, *ProcessServices, full backend maps,
// database pool registries, or an any services map (Requirement 8.4, Task 2.1).
type ProcessInput struct {
	Logger         *slog.Logger
	ExtensionState lipstate.Store
	BackgroundAux  *auxreq.BackgroundScheduler
	// buildSteps carries staged construction actions for package-local tests
	// only. It is unexported so no external caller (including generic
	// runtimebundle) can inject constructors or closers (Tasks 2.1/2.3).
	buildSteps []constructionStep
}

// CorePorts carries minimal fixed consumer-owned core interfaces needed by Tasks 3-7.
// It is a fixed internal adapter, NOT a service map (design §7, Requirement 8.3).
type CorePorts struct {
	CompactionDetector runtime.CompactionDetector
}

// GenerationInput carries inputs for featurehost generation composition.
// It deliberately does NOT accept *runtimebundle.BuildOptions or *runtimebundle.ProcessServices.
type GenerationInput struct {
	Registrations    []lipsdk.Registration
	MergeSurface     featurebundle.GeneratedMergeSurface
	Planes           lipfeature.FrozenPlaneSet
	Lifecycles       []lipplugin.Lifecycle
	CandidatePlanes  lipfeature.FrozenPlaneSet
	BackgroundClient auxiliary.BackgroundClient
	BackgroundPoller auxiliary.BackgroundPoller
	// ReasoningProdOpts/ReasoningTestOpts carry the raw production and testing
	// reasoning option sources. The facade merges them internally (Task 2.4,
	// Requirement 8.3); generic runtimebundle must never merge or interpret
	// reasoning policy itself, so no merged ReasoningOpts field exists here.
	ReasoningProdOpts ReasoningCompressionOptions
	ReasoningTestOpts ReasoningCompressionOptions
	AccessMode        accessmode.Mode
	SecretEnv         SecretGuardEnvironment
	SecretInputs      SecretGuardInputs
	DecisionObserver  SecretDecisionObserver
	FaultInject       error
}

// GenerationOutput represents the compiled output of standard-distribution features
// for a single generation.
type GenerationOutput struct {
	Bundle               lipfeature.FeatureBundle
	Planes               lipfeature.FrozenPlaneSet
	Lifecycles           []lipplugin.Lifecycle
	SecretGuard          extensions.SecretGuardPlane
	SecretGuardInventory *diag.InventoryExtras
	CorePorts            CorePorts
}
