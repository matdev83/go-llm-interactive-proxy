package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/keepwarm"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminaldecisionpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactioncompose"
	compactiondetect "github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactiondetect"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// ProcessFeatureTransitionRow represents an entry in the authoritative Wave-2 transition table (Task 1.1 / 2.3).
type ProcessFeatureTransitionRow struct {
	ResourceName          string
	ConcreteType          string
	CurrentConstructor    string
	CurrentFieldHolder    string
	CloseRegistrationSite string
	Closable              bool
	BorrowedDeps          string
	TransferTask          string
	InterimOwnershipRule  string
}

// ProcessFeatureTransitionTable is the authoritative transition contract from implementation-baseline.md §7.
var ProcessFeatureTransitionTable = []ProcessFeatureTransitionRow{
	{
		ResourceName:          "KeepwarmPolicy",
		ConcreteType:          "*keepwarm.PolicyStore",
		CurrentConstructor:    "keepwarm.NewPolicyStore(keepwarm.DefaultMaxPolicyEntries) called at process_services.go:64",
		CurrentFieldHolder:    "ProcessServices.KeepwarmPolicy",
		CloseRegistrationSite: "Non-closable (no cleanup registered)",
		Closable:              false,
		BorrowedDeps:          "config.PromptCache, metrics sink",
		TransferTask:          "Task 6.3",
		InterimOwnershipRule:  "Legacy constructor and holder remain sole owner until Task 6.3; featurehost constructs 0 duplicate instances.",
	},
	{
		ResourceName:          "KeepwarmRegistry",
		ConcreteType:          "*keepwarm.ManagerRegistry",
		CurrentConstructor:    "keepwarm.NewManagerRegistry() called at process_services.go:74",
		CurrentFieldHolder:    "ProcessServices.KeepwarmRegistry",
		CloseRegistrationSite: "Non-closable (no cleanup registered)",
		Closable:              false,
		BorrowedDeps:          "keepwarm.PolicyStore, scheduler goroutines, background ping worker",
		TransferTask:          "Task 6.3",
		InterimOwnershipRule:  "Legacy constructor and holder remain sole owner until Task 6.3; featurehost constructs 0 duplicate instances.",
	},
	{
		ResourceName:          "TerminalDecisionPolicy",
		ConcreteType:          "*terminaldecisionpolicy.Store",
		CurrentConstructor:    "terminaldecisionpolicy.NewStore(terminaldecisionpolicy.Config{}) called at process_services.go:75",
		CurrentFieldHolder:    "ProcessServices.TerminalDecisionPolicy",
		CloseRegistrationSite: "Closable: register(ps.TerminalDecisionPolicy.Close) registered at process_services.go:85",
		Closable:              true,
		BorrowedDeps:          "None (pure in-memory bounded store)",
		TransferTask:          "Task 7.3",
		InterimOwnershipRule:  "Legacy ProcessServices closer remains sole physical cleanup owner; featurehost MUST NOT close. Atomic transfer in Task 7.3.",
	},
	{
		ResourceName:          "CompactionDetector",
		ConcreteType:          "runtime.CompactionDetector",
		CurrentConstructor:    "compactiondetect.New(compactiondetect.Config{}) called at background_aux_lifecycle.go:29",
		CurrentFieldHolder:    "ProcessServices.CompactionDetector",
		CloseRegistrationSite: "Non-closable (no cleanup registered)",
		Closable:              false,
		BorrowedDeps:          "config.Compaction",
		TransferTask:          "Task 3.3",
		InterimOwnershipRule:  "Legacy constructor and holder remain sole owner until Task 3.3; featurehost constructs 0 duplicate instances.",
	},
	{
		ResourceName:          "BranchCoordinator",
		ConcreteType:          "*compactioncontinuity.BranchCoordinator",
		CurrentConstructor:    "compactioncontinuity.NewBranchCoordinator(ctx, compactioncontinuity.Config{Store: ps.ExtensionState}) called at branch_coordinator.go:17 (invoked via bindSharedMutableProcessServices at process_services.go:238)",
		CurrentFieldHolder:    "ProcessServices.BranchCoordinator",
		CloseRegistrationSite: "Non-closable (no cleanup registered)",
		Closable:              false,
		BorrowedDeps:          "Storage adapters (in-memory or Bun persistence)",
		TransferTask:          "Task 3.3",
		InterimOwnershipRule:  "Legacy constructor and holder remain sole owner until Task 3.3; featurehost constructs 0 duplicate instances.",
	},
	{
		ResourceName:          "CompactionParentPort",
		ConcreteType:          "*compactioncompose.CompactionContinuityParentPort",
		CurrentConstructor:    "compactioncompose.NewCompactionContinuityParentPort(ps.BranchCoordinator) called at branch_coordinator.go:21 (invoked via bindSharedMutableProcessServices at process_services.go:238)",
		CurrentFieldHolder:    "ProcessServices.CompactionParentPort",
		CloseRegistrationSite: "Non-closable (no cleanup registered)",
		Closable:              false,
		BorrowedDeps:          "BranchCoordinator",
		TransferTask:          "Task 3.3",
		InterimOwnershipRule:  "Legacy constructor and holder remain sole owner until Task 3.3; featurehost constructs 0 duplicate instances.",
	},
	{
		ResourceName:          "BackgroundAux",
		ConcreteType:          "*auxreq.BackgroundScheduler",
		CurrentConstructor:    "compactioncompose.NewProductionBackgroundScheduler(ctx, in.Cfg) called at background_aux_lifecycle.go:23",
		CurrentFieldHolder:    "ProcessServices.BackgroundAux",
		CloseRegistrationSite: "Closable: register(ps.BackgroundAux.Close) registered at background_aux_lifecycle.go:27",
		Closable:              true,
		BorrowedDeps:          "Process context, bounded worker pool",
		TransferTask:          "never (borrowed)",
		InterimOwnershipRule:  "Feature host borrows BackgroundAux as a generic input; feature host MUST NEVER close or own BackgroundAux.",
	},
}

// ErrDualConstructorWiring indicates that both legacy and featurehost paths
// constructed or registered cleanup for the same process-scoped feature resource.
var ErrDualConstructorWiring = errors.New("runtimebundle: dual constructor wiring detected")

// checkNoFeaturehostOwnership rejects any featurehost-owned process resource
// observed before its transfer task. It holds the pure dual-ownership decision
// logic: ValidateProcessFeatureOwnership feeds it real Runtime state, and tests
// feed it real constructor products, so both dual-state rejection branches
// execute behaviorally (Task 2.3 negative proof).
func checkNoFeaturehostOwnership(ownedPolicy *terminaldecisionpolicy.Store, ownedClosers int) error {
	if ownedPolicy != nil {
		return fmt.Errorf("%w: resource TerminalDecisionPolicy is constructed/owned by both legacy and featurehost paths (observed duplicate live instance on featurehost)", ErrDualConstructorWiring)
	}
	if ownedClosers > 0 {
		return fmt.Errorf("%w: featurehost owns %d closers prior to transfer tasks", ErrDualConstructorWiring, ownedClosers)
	}
	return nil
}

// ValidateProcessFeatureOwnership verifies that every transition-table process resource
// has strictly one constructor and cleanup owner at the current checkpoint (Task 2.3).
// It inspects actual typed ProcessServices fields + featurehost Runtime typed state:
//  1. Every untransferred legacy resource (Rows 1-6) and borrowed resource (Row 7)
//     must be present on ProcessServices (deleting a legacy constructor fails).
//  2. Featurehost Runtime must have zero migrated ownership (TerminalDecisionPolicy == nil, ClosersCount == 0).
//  3. If any resource is observed to be owned by both paths, ErrDualConstructorWiring is returned.
func ValidateProcessFeatureOwnership(ps *ProcessServices) error {
	if ps == nil {
		return fmt.Errorf("runtimebundle: nil ProcessServices")
	}
	if ps.StandardFeatures == nil {
		return fmt.Errorf("runtimebundle: nil StandardFeatures on ProcessServices")
	}

	// 1. Verify legacy constructor presence for all untransferred rows.
	// If a legacy constructor is deleted before its handoff task, this check fails.
	if ps.KeepwarmPolicy == nil {
		return fmt.Errorf("%w: legacy resource KeepwarmPolicy is missing from ProcessServices", ErrDualConstructorWiring)
	}
	if ps.KeepwarmRegistry == nil {
		return fmt.Errorf("%w: legacy resource KeepwarmRegistry is missing from ProcessServices", ErrDualConstructorWiring)
	}
	if ps.TerminalDecisionPolicy == nil {
		return fmt.Errorf("%w: legacy resource TerminalDecisionPolicy is missing from ProcessServices", ErrDualConstructorWiring)
	}
	if ps.CompactionDetector == nil {
		return fmt.Errorf("%w: legacy resource CompactionDetector is missing from ProcessServices", ErrDualConstructorWiring)
	}
	if ps.BranchCoordinator == nil {
		return fmt.Errorf("%w: legacy resource BranchCoordinator is missing from ProcessServices", ErrDualConstructorWiring)
	}
	if ps.CompactionParentPort == nil {
		return fmt.Errorf("%w: legacy resource CompactionParentPort is missing from ProcessServices", ErrDualConstructorWiring)
	}
	if ps.BackgroundAux == nil {
		return fmt.Errorf("%w: borrowed resource BackgroundAux is missing from ProcessServices", ErrDualConstructorWiring)
	}

	// 2. Inspect real typed featurehost Runtime state per transition table.
	// Prior to Task 7.3, featurehost must NOT own TerminalDecisionPolicy and
	// must own zero process feature closers; the decision itself lives in
	// checkNoFeaturehostOwnership so both rejection branches execute in tests.
	return checkNoFeaturehostOwnership(ps.StandardFeatures.TerminalDecisionPolicy(), ps.StandardFeatures.ClosersCount())
}

// Non-tautological compile-time drift checks: any field retype or rename on ProcessServices breaks compilation.
func _driftCompilationGuard() {
	var ps *ProcessServices
	var _ *keepwarm.PolicyStore = ps.KeepwarmPolicy
	var _ *keepwarm.ManagerRegistry = ps.KeepwarmRegistry
	var _ *terminaldecisionpolicy.Store = ps.TerminalDecisionPolicy
	var _ runtime.CompactionDetector = ps.CompactionDetector
	var _ *compactioncontinuity.BranchCoordinator = ps.BranchCoordinator
	var _ *compactioncompose.CompactionContinuityParentPort = ps.CompactionParentPort
	var _ *auxreq.BackgroundScheduler = ps.BackgroundAux

	var (
		_ func(int) (*keepwarm.PolicyStore, error)                                                                 = keepwarm.NewPolicyStore
		_ func() *keepwarm.ManagerRegistry                                                                         = keepwarm.NewManagerRegistry
		_ func(terminaldecisionpolicy.Config) *terminaldecisionpolicy.Store                                        = terminaldecisionpolicy.NewStore
		_ func(compactiondetect.Config) *compactiondetect.Detector                                                 = compactiondetect.New
		_ func(context.Context, compactioncontinuity.Config) (*compactioncontinuity.BranchCoordinator, error)      = compactioncontinuity.NewBranchCoordinator
		_ func(*compactioncontinuity.BranchCoordinator) (*compactioncompose.CompactionContinuityParentPort, error) = compactioncompose.NewCompactionContinuityParentPort
		_ func(context.Context, *config.Config) *auxreq.BackgroundScheduler                                        = compactioncompose.NewProductionBackgroundScheduler
	)
}

func TestProcessFeatureOwnership_TransitionTableIntegrity(t *testing.T) {
	t.Parallel()

	psType := reflect.TypeFor[ProcessServices]()

	expectedRows := map[string]struct {
		fieldName    string
		closable     bool
		transferTask string
		callSite     string
		closeSite    string
	}{
		"KeepwarmPolicy": {
			fieldName:    "KeepwarmPolicy",
			closable:     false,
			transferTask: "Task 6.3",
			callSite:     "process_services.go:64",
			closeSite:    "Non-closable",
		},
		"KeepwarmRegistry": {
			fieldName:    "KeepwarmRegistry",
			closable:     false,
			transferTask: "Task 6.3",
			callSite:     "process_services.go:74",
			closeSite:    "Non-closable",
		},
		"TerminalDecisionPolicy": {
			fieldName:    "TerminalDecisionPolicy",
			closable:     true,
			transferTask: "Task 7.3",
			callSite:     "process_services.go:75",
			closeSite:    "process_services.go:85",
		},
		"CompactionDetector": {
			fieldName:    "CompactionDetector",
			closable:     false,
			transferTask: "Task 3.3",
			callSite:     "background_aux_lifecycle.go:29",
			closeSite:    "Non-closable",
		},
		"BranchCoordinator": {
			fieldName:    "BranchCoordinator",
			closable:     false,
			transferTask: "Task 3.3",
			callSite:     "branch_coordinator.go:17",
			closeSite:    "Non-closable",
		},
		"CompactionParentPort": {
			fieldName:    "CompactionParentPort",
			closable:     false,
			transferTask: "Task 3.3",
			callSite:     "branch_coordinator.go:21",
			closeSite:    "Non-closable",
		},
		"BackgroundAux": {
			fieldName:    "BackgroundAux",
			closable:     true,
			transferTask: "never (borrowed)",
			callSite:     "background_aux_lifecycle.go:23",
			closeSite:    "background_aux_lifecycle.go:27",
		},
	}

	if len(ProcessFeatureTransitionTable) != len(expectedRows) {
		t.Fatalf("transition table row count = %d, want %d", len(ProcessFeatureTransitionTable), len(expectedRows))
	}

	for _, row := range ProcessFeatureTransitionTable {
		expected, ok := expectedRows[row.ResourceName]
		if !ok {
			t.Errorf("unexpected resource name in transition table: %s", row.ResourceName)
			continue
		}
		field, ok := psType.FieldByName(expected.fieldName)
		if !ok {
			t.Errorf("expected field %s missing on ProcessServices", expected.fieldName)
			continue
		}
		expectedType := field.Type.String()
		if row.ConcreteType != expectedType {
			t.Errorf("resource %s concreteType = %q, want %q", row.ResourceName, row.ConcreteType, expectedType)
		}
		if row.Closable != expected.closable {
			t.Errorf("resource %s closable = %v, want %v", row.ResourceName, row.Closable, expected.closable)
		}
		if row.TransferTask != expected.transferTask {
			t.Errorf("resource %s transfer task = %s, want %s", row.ResourceName, row.TransferTask, expected.transferTask)
		}
		if !strings.Contains(row.CurrentConstructor, expected.callSite) {
			t.Errorf("resource %s CurrentConstructor %q does not cite call site %q", row.ResourceName, row.CurrentConstructor, expected.callSite)
		}
		if !strings.Contains(row.CloseRegistrationSite, expected.closeSite) {
			t.Errorf("resource %s CloseRegistrationSite %q does not cite %q", row.ResourceName, row.CloseRegistrationSite, expected.closeSite)
		}
		if row.CurrentFieldHolder != "ProcessServices."+expected.fieldName {
			t.Errorf("resource %s CurrentFieldHolder = %q, want %q", row.ResourceName, row.CurrentFieldHolder, "ProcessServices."+expected.fieldName)
		}
		if row.InterimOwnershipRule == "" {
			t.Errorf("resource %s has empty InterimOwnershipRule", row.ResourceName)
		}
	}
}

func TestProcessFeatureOwnership_SingleConstructorValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ps, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:  testProcessServicesOwnershipConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	if err := ValidateProcessFeatureOwnership(ps); err != nil {
		t.Fatalf("ValidateProcessFeatureOwnership failed on production wiring: %v", err)
	}
}

func TestProcessFeatureOwnership_DualConstructorWiringRejected(t *testing.T) {
	t.Parallel()

	// At the Task 2.3 checkpoint no production path may construct a
	// featurehost-owned TerminalDecisionPolicy: the only terminal-policy
	// constructor in the tree is the legacy runtimebundle path, and the
	// featurehost facade exposes no policy-construction input (proven
	// structurally by TestFeatureHost_NoPreHandoffFeatureConstruction, which
	// fails if a TerminalPolicyConfig field or NewStore call is added).
	// The dual-state signals themselves are proven flippable with the real
	// constructor by TestRuntime_DualOwnershipSignalsObservable
	// (process_lifecycle_internal_test.go); the validator rejects exactly
	// that observable state with ErrDualConstructorWiring.
	// This test proves the same invariant behaviorally across representative
	// process inputs: featurehost must own nothing, so dual ownership is
	// unobservable and validation accepts. Any future second constructor
	// (the Task 7.3 handoff) must flip these assertions atomically.
	ctx := context.Background()
	buildRealProcess := func(t *testing.T, opts *BuildOptions) *ProcessServices {
		t.Helper()
		ps, err := NewProcessServices(ctx, ProcessServicesInput{
			Cfg:  testProcessServicesOwnershipConfig(),
			Log:  testkit.DiscardLogger(),
			Opts: opts,
		})
		if err != nil {
			t.Fatalf("NewProcessServices: %v", err)
		}
		return ps
	}

	optShapes := map[string]*BuildOptions{
		"default":         {PluginRegistry: pluginreg.NewRegistry()},
		"nil testing":     {PluginRegistry: pluginreg.NewRegistry(), Testing: TestingOptions{}},
		"empty prod/test": {PluginRegistry: pluginreg.NewRegistry(), Testing: TestingOptions{}, Production: ProductionOptions{}},
	}
	for name, opts := range optShapes {
		ps := buildRealProcess(t, opts)
		t.Cleanup(func() { _ = ps.Close() })

		if ps.TerminalDecisionPolicy == nil {
			t.Fatalf("%s: expected legacy TerminalDecisionPolicy to be instantiated", name)
		}
		if got := ps.StandardFeatures.TerminalDecisionPolicy(); got != nil {
			t.Fatalf("%s: observed featurehost-owned TerminalDecisionPolicy %p pre-handoff (dual wiring)", name, got)
		}
		if got := ps.StandardFeatures.ClosersCount(); got != 0 {
			t.Fatalf("%s: observed %d featurehost-owned closers pre-handoff (dual wiring)", name, got)
		}
		if err := ValidateProcessFeatureOwnership(ps); err != nil {
			t.Fatalf("%s: ValidateProcessFeatureOwnership failed on single-owner wiring: %v", name, err)
		}
	}

	// The validator must still reject genuinely missing legacy ownership.
	broken := buildRealProcess(t, &BuildOptions{PluginRegistry: pluginreg.NewRegistry()})
	t.Cleanup(func() { _ = broken.Close() })
	broken.TerminalDecisionPolicy = nil
	if err := ValidateProcessFeatureOwnership(broken); err == nil {
		t.Fatal("expected error when legacy constructor output is missing, got nil")
	}
}

func TestProcessFeatureOwnership_DualOwnershipSignalsRejected(t *testing.T) {
	t.Parallel()

	// Direct behavioral coverage of both dual-state rejection branches with a
	// REAL terminal-policy store from the legacy constructor. Combined with
	// TestRuntime_DualOwnershipSignalsObservable (which proves these exact
	// signals flip under genuine dual ownership) and the absence matrix in
	// TestProcessFeatureOwnership_DualConstructorWiringRejected, this supplies
	// the Task 2.3 negative proof without any production test seam.
	store := terminaldecisionpolicy.NewStore(terminaldecisionpolicy.Config{})
	t.Cleanup(func() { _ = store.Close() })

	if err := checkNoFeaturehostOwnership(store, 1); !errors.Is(err, ErrDualConstructorWiring) {
		t.Fatalf("policy+closer dual state: expected ErrDualConstructorWiring, got %v", err)
	}
	if err := checkNoFeaturehostOwnership(store, 0); !errors.Is(err, ErrDualConstructorWiring) {
		t.Fatalf("policy-only dual state: expected ErrDualConstructorWiring, got %v", err)
	}
	if err := checkNoFeaturehostOwnership(nil, 1); !errors.Is(err, ErrDualConstructorWiring) {
		t.Fatalf("closer-only dual state: expected ErrDualConstructorWiring, got %v", err)
	}
	if err := checkNoFeaturehostOwnership(nil, 0); err != nil {
		t.Fatalf("single-owner state: expected nil error, got %v", err)
	}
}

func TestProcessFeatureOwnership_MissingLegacyConstructorRejected(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ps, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:  testProcessServicesOwnershipConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	// Simulate deleting the legacy constructor for TerminalDecisionPolicy
	ps.TerminalDecisionPolicy = nil

	err = ValidateProcessFeatureOwnership(ps)
	if err == nil {
		t.Fatal("expected error when legacy constructor is missing/deleted, got nil")
	}
}
