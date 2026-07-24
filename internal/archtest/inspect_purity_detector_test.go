package archtest

import (
	"os"
	"path/filepath"
	"testing"
)

// Task 5.3 synthetic detector proofs for gateInspectPurity. Fixtures are
// in-memory only; they must not mutate repository source.

func TestInspectPurity_DirectBuildHostInInspectRoutesRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func InspectRoutes() {
	_, _ = BuildHost(nil, BuildHostInput{})
}
`
	got, err := scanInspectPuritySource("internal/infra/runtimebundle/inspect.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:InspectRoutes->runtimebundle.BuildHost#1") {
		t.Fatalf("expected direct BuildHost in InspectRoutes, got %v", got)
	}
}

func TestInspectPurity_LocalAliasInPrepareInspectRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func prepareInspect() {
	build := BuildHost
	_, _ = build(nil, BuildHostInput{})
}
`
	got, err := scanInspectPuritySource("internal/infra/runtimebundle/inspect.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:prepareInspect->runtimebundle.BuildHost#1") {
		t.Fatalf("expected local BuildHost alias in prepareInspect, got %v", got)
	}
}

func TestInspectPurity_PackageScopeAliasInInspectInventoryRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
var build = BuildHost
func InspectInventory() {
	_, _ = build(nil, BuildHostInput{})
}
`
	got, err := scanInspectPuritySource("internal/infra/runtimebundle/inspect.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:InspectInventory->runtimebundle.BuildHost#1") {
		t.Fatalf("expected package-scope BuildHost alias in InspectInventory, got %v", got)
	}
}

func TestInspectPurity_RenamedWrapperDelegatingToProcessServicesRejected(t *testing.T) {
	t.Parallel()
	// Hermes-style bypass: Inspect calls a renamed wrapper whose body
	// delegates to NewProcessServices / BuildHost.
	src := `package runtimebundle
func assembleOperatorView() {
	_ = NewProcessServices(nil, nil)
	_, _ = BuildHost(nil, BuildHostInput{})
}
func InspectRoutes() {
	assembleOperatorView()
}
`
	got, err := scanInspectPuritySource("internal/infra/runtimebundle/inspect.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:InspectRoutes->assembleOperatorView#1") {
		t.Fatalf("expected renamed wrapper call from InspectRoutes, got %v", got)
	}
}

func TestInspectPurity_CLILocalWrapperDelegatingToBuildBootstrapRejected(t *testing.T) {
	t.Parallel()
	src := `package main
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func routesViaLegacy() {
	_, _ = runtimebundle.BuildBootstrap(nil, runtimebundle.BuildBootstrapInput{})
}
func runRoutesCommand() {
	routesViaLegacy()
}
`
	got, err := scanInspectPuritySource("cmd/lipstd/command.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:runRoutesCommand->routesViaLegacy#1") {
		t.Fatalf("expected CLI local wrapper call detection, got %v", got)
	}
}

func TestInspectPurity_FocusedInspectImplementationNotFlagged(t *testing.T) {
	t.Parallel()
	// Negative control: focused strict loader + registry install + projection.
	src := `package runtimebundle
func LoadBootstrapEffectiveWithSource() {}
func installRegistryAndRegistrations() {}
func RoutesSnapshotFrom() {}
func InventorySnapshotForOperator() {}
func prepareInspect() {
	LoadBootstrapEffectiveWithSource()
	installRegistryAndRegistrations()
}
func InspectRoutes() {
	prepareInspect()
	RoutesSnapshotFrom()
}
func InspectInventory() {
	prepareInspect()
	InventorySnapshotForOperator()
}
`
	got, err := scanInspectPuritySource("internal/infra/runtimebundle/inspect.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("focused Inspect implementation must not be findings, got %v", got)
	}
}

func TestInspectPurity_SyntheticBuildBootstrapCallRejected(t *testing.T) {
	t.Parallel()
	src := `package main
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func runRoutesCommand() {
	_, _ = runtimebundle.BuildBootstrap(nil, runtimebundle.BuildBootstrapInput{})
}
`
	got, err := scanInspectPuritySource("cmd/lipstd/synthetic_routes.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:runRoutesCommand->runtimebundle.BuildBootstrap#1") {
		t.Fatalf("expected BuildBootstrap call detection, got %v", got)
	}
}

func TestInspectPurity_SyntheticGenerationManagerCallRejected(t *testing.T) {
	t.Parallel()
	src := `package main
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
func runInventoryCommand() {
	_ = runtimehost.NewManager(0, nil)
}
`
	got, err := scanInspectPuritySource("cmd/lipstd/synthetic_manager.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:runInventoryCommand->runtimehost.NewManager#1") {
		t.Fatalf("expected runtimehost.NewManager call detection, got %v", got)
	}
}

func TestInspectPurity_OwnershipImportInInspectFileRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
import (
	"log/slog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
)
func InspectRoutes() {}
`
	got, err := scanInspectPuritySource("internal/infra/runtimebundle/inspect.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, id := range []string{"import:slog", "import:runtimehost", "import:tracing", "import:stdhttp"} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("expected %s, got %v", id, got)
		}
	}
}

func TestInspectPurity_UnrelatedFunctionNotScanned(t *testing.T) {
	t.Parallel()
	// Negative control: BuildBootstrap/BuildHost remain legitimate for
	// check-config/serve; only Inspect-role bodies are scanned.
	src := `package main
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func runCheckConfigCommand() {
	_, _ = runtimebundle.BuildBootstrap(nil, runtimebundle.BuildBootstrapInput{})
}
func runServeCommand() {
	_, _ = runtimebundle.BuildHost(nil, runtimebundle.BuildHostInput{})
}
`
	got, err := scanInspectPuritySource("cmd/lipstd/synthetic_unrelated.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("check-config/serve command bodies must not be scanned, got %v", got)
	}
}

func TestInspectPurity_RealInspectAndCommandFilesAreClean(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{
		filepath.Join("cmd", "lipstd", "command.go"),
		filepath.Join("internal", "infra", "runtimebundle", "inspect.go"),
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		got, err := scanInspectPuritySource(filepath.ToSlash(rel), string(raw))
		if err != nil {
			t.Fatalf("scan %s: %v", rel, err)
		}
		if len(got) != 0 {
			t.Fatalf("expected no findings in %s, got %v", rel, got)
		}
	}
}
