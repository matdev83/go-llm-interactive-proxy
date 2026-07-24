package archtest

import (
	"os"
	"path/filepath"
	"testing"
)

// Task 5.4 synthetic detector proofs for gateValidationPurity. Fixtures are
// in-memory only; they must not mutate repository source.

func TestValidationPurity_DirectBuildHostInValidateDistributionRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func validateDistribution() {
	_, _ = BuildHost(nil, BuildHostInput{})
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->runtimebundle.BuildHost#1") {
		t.Fatalf("expected direct BuildHost in validateDistribution, got %v", got)
	}
}

func TestValidationPurity_DirectManagerConstructionRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
func validateDistribution() {
	_ = runtimehost.NewManager(0, nil)
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->runtimehost.NewManager#1") {
		t.Fatalf("expected runtimehost.NewManager detection, got %v", got)
	}
}

func TestValidationPurity_PublishMethodCallRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func validateDistribution() {
	var mgr *Manager
	_ = mgr.Publish(nil)
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->Publish#1") {
		t.Fatalf("expected Publish method-call detection, got %v", got)
	}
}

func TestValidationPurity_PublishMethodValueAliasRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func validateDistribution() {
	var mgr *Manager
	publish := mgr.Publish
	_ = publish(nil)
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->Publish#1") {
		t.Fatalf("expected Publish method-value alias detection, got %v", got)
	}
}

func TestValidationPurity_PublishInitialGenerationRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func validateDistribution() {
	_, _ = publishInitialGeneration(nil, BootstrapResult{}, publishInitialGenerationInput{})
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->runtimebundle.publishInitialGeneration#1") {
		t.Fatalf("expected publishInitialGeneration detection, got %v", got)
	}
}

func TestValidationPurity_PublishInitialGenerationLocalAliasRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func validateDistribution() {
	pub := publishInitialGeneration
	_, _ = pub(nil, BootstrapResult{}, publishInitialGenerationInput{})
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->runtimebundle.publishInitialGeneration#1") {
		t.Fatalf("expected publishInitialGeneration local alias detection, got %v", got)
	}
}

func TestValidationPurity_BindReloadHostRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func validateDistribution() {
	_, _ = bindReloadHost("", bindReloadHostInput{})
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->runtimebundle.bindReloadHost#1") {
		t.Fatalf("expected bindReloadHost detection, got %v", got)
	}
}

func TestValidationPurity_NewReloadHostAndCoordinatorRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func validateDistribution() {
	_ = NewReloadHost()
	_ = NewReloadCoordinator()
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, id := range []string{
		"call:validateDistribution->runtimebundle.NewReloadHost#1",
		"call:validateDistribution->runtimebundle.NewReloadCoordinator#1",
	} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("expected %s, got %v", id, got)
		}
	}
}

func TestValidationPurity_RenamedWrapperDelegatingToBindReloadHostRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func wireValidationReload() {
	_, _ = bindReloadHost("", bindReloadHostInput{})
}
func validateDistribution() {
	wireValidationReload()
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->wireValidationReload#1") {
		t.Fatalf("expected renamed bindReloadHost wrapper detection, got %v", got)
	}
}

func TestValidationPurity_NewBootstrapAppRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func validateDistribution() {
	_, _ = NewBootstrapApp(BootstrapOptions{})
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->runtimebundle.NewBootstrapApp#1") {
		t.Fatalf("expected NewBootstrapApp detection, got %v", got)
	}
}

func TestValidationPurity_NewBootstrapAppLocalAliasRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func validateDistribution() {
	boot := NewBootstrapApp
	_, _ = boot(BootstrapOptions{})
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->runtimebundle.NewBootstrapApp#1") {
		t.Fatalf("expected NewBootstrapApp local alias detection, got %v", got)
	}
}

func TestValidationPurity_ListenConfigListenMethodRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
import "net"
func validateDistribution() {
	var lc net.ListenConfig
	_, _ = lc.Listen(nil, "tcp", "127.0.0.1:0")
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->Listen#1") {
		t.Fatalf("expected ListenConfig.Listen method detection, got %v", got)
	}
}

func TestValidationPurity_ListenMethodValueAliasRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
import "net"
func validateDistribution() {
	var lc net.ListenConfig
	listen := lc.Listen
	_, _ = listen(nil, "tcp", "127.0.0.1:0")
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->Listen#1") {
		t.Fatalf("expected Listen method-value alias detection, got %v", got)
	}
}

func TestValidationPurity_HTTPServerListenAndServeRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
import "net/http"
func validateDistribution() {
	var srv http.Server
	_ = srv.ListenAndServe()
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->ListenAndServe#1") {
		t.Fatalf("expected http.Server.ListenAndServe detection, got %v", got)
	}
}

func TestValidationPurity_RuntimehostNewGenerationRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
func validateDistribution() {
	_ = runtimehost.NewGeneration()
	_ = runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{})
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, id := range []string{
		"call:validateDistribution->runtimehost.NewGeneration#1",
		"call:validateDistribution->runtimehost.NewCoordinator#1",
	} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("expected %s, got %v", id, got)
		}
	}
}

func TestValidationPurity_PrepareRequestPlaneMethodValueAliasRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func validateDistribution() {
	var mgr *Manager
	prep := mgr.PrepareRequestPlane
	_, _ = prep(nil)
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->PrepareRequestPlane#1") {
		t.Fatalf("expected PrepareRequestPlane method-value alias detection, got %v", got)
	}
}

func TestValidationPurity_LocalAliasInValidateDistributionRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func validateDistribution() {
	build := BuildBootstrap
	_, _ = build(nil, BuildBootstrapInput{})
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->runtimebundle.BuildBootstrap#1") {
		t.Fatalf("expected local BuildBootstrap alias in validateDistribution, got %v", got)
	}
}

func TestValidationPurity_RenamedWrapperDelegatingToAttachReloadHostRejected(t *testing.T) {
	t.Parallel()
	// Hermes-style bypass: validateDistribution calls a renamed wrapper whose
	// body delegates to AttachReloadHost.
	src := `package runtimebundle
func assembleValidationHost() {
	_, _ = AttachReloadHost(nil, "", nil)
}
func validateDistribution() {
	assembleValidationHost()
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->assembleValidationHost#1") {
		t.Fatalf("expected renamed wrapper call from validateDistribution, got %v", got)
	}
}

func TestValidationPurity_CLILocalWrapperDelegatingToBuildHostRejected(t *testing.T) {
	t.Parallel()
	src := `package main
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func checkConfigViaLegacy() {
	_, _ = runtimebundle.BuildHost(nil, runtimebundle.BuildHostInput{})
}
func runCheckConfigCommand() {
	checkConfigViaLegacy()
}
`
	got, err := scanValidationPuritySource("cmd/lipstd/command.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:runCheckConfigCommand->checkConfigViaLegacy#1") {
		t.Fatalf("expected CLI local wrapper call detection, got %v", got)
	}
}

func TestValidationPurity_NetListenInValidateDistributionRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
import "net"
func validateDistribution() {
	_, _ = net.Listen("tcp", "127.0.0.1:0")
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:validateDistribution->net.Listen#1") {
		t.Fatalf("expected net.Listen detection, got %v", got)
	}
}

func TestValidationPurity_OwnershipImportInValidateOpsFileRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
)
func validateDistribution() {}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, id := range []string{"import:runtimehost", "import:stdhttp"} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("expected %s, got %v", id, got)
		}
	}
}

// TestValidationPurity_ProcessServicesAndCompileGenerationNotFlagged is the
// key negative control distinguishing this gate from gateInspectPurity:
// ValidateDistribution legitimately uses NewProcessServices/CompileGeneration.
func TestValidationPurity_ProcessServicesAndCompileGenerationNotFlagged(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func validateDistribution() {
	ps, _ := NewProcessServices(nil, ProcessServicesInput{})
	_, _ = CompileGeneration(nil, GenerationCompileInput{Process: ps})
}
`
	got, err := scanValidationPuritySource("internal/infra/runtimebundle/validate_distribution.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("NewProcessServices/CompileGeneration must not be findings, got %v", got)
	}
}

func TestValidationPurity_UnrelatedServeFunctionNotScanned(t *testing.T) {
	t.Parallel()
	// Negative control: serve/BuildHost callers outside the validation role
	// surface are legitimate and must not be scanned by this gate.
	src := `package main
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func runServeCommand() {
	_, _ = runtimebundle.BuildHost(nil, runtimebundle.BuildHostInput{})
}
`
	got, err := scanValidationPuritySource("cmd/lipstd/synthetic_unrelated.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("serve command body must not be scanned, got %v", got)
	}
}

func TestValidationPurity_RealValidateOpsAndCommandFilesAreClean(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{
		filepath.Join("cmd", "lipstd", "command.go"),
		filepath.Join("internal", "infra", "runtimebundle", "validate_distribution.go"),
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		got, err := scanValidationPuritySource(filepath.ToSlash(rel), string(raw))
		if err != nil {
			t.Fatalf("scan %s: %v", rel, err)
		}
		if len(got) != 0 {
			t.Fatalf("expected no findings in %s, got %v", rel, got)
		}
	}
}
