package archtest

import (
	"strings"
	"testing"
)

// Synthetic detector proofs for Task 1.4. Fixtures are in-memory only; they must
// not mutate repository source. Each gate family proves at least one new
// forbidden production pattern is detected.
//
// Parser-only fixtures are labeled where noted: they are valid Go AST shapes
// matching protected identities closely enough for scanner coverage, but are not
// compiled as a package.

func TestRuntimeConvergence_SyntheticNewRequestPlaneAsBuiltRejected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func requestPlaneAsBuilt(plane runtimebundle.RequestPlane) *runtimebundle.Built {
	return &runtimebundle.Built{}
}
func ComposeRequestPlane(plane runtimebundle.RequestPlane) {
	_ = requestPlaneAsBuilt(plane)
}
`
	got, err := scanRuntimeConvergenceSource("internal/stdhttp/synthetic_request_plane.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "func:requestPlaneAsBuilt") {
		t.Fatalf("expected adapter declaration detection, got %v", got)
	}
	if !findingsContainIdentity(got, "call:ComposeRequestPlane->requestPlaneAsBuilt#1") {
		t.Fatalf("expected call detection, got %v", got)
	}
}

func TestRuntimeConvergence_SyntheticNewRunWithRuntimeCallRejected(t *testing.T) {
	t.Parallel()
	src := `package cmd
import "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
func serve() { _ = stdhttp.RunWithRuntime(nil, nil, nil, nil, nil) }
`
	got, err := scanRuntimeConvergenceSource("cmd/lipstd/synthetic_serve.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:serve->stdhttp.RunWithRuntime#1") {
		t.Fatalf("expected RunWithRuntime call detection, got %v", got)
	}
}

func TestRuntimeConvergence_SyntheticNewRuntimebundleBuildCallRejected(t *testing.T) {
	t.Parallel()
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func wire() { _, _ = runtimebundle.Build(nil, nil, nil, nil) }
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/synthetic_build.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:wire->runtimebundle.Build#1") {
		t.Fatalf("expected runtimebundle.Build call detection, got %v", got)
	}
}

func TestRuntimeConvergence_CommentAndUnrelatedBuildNotFlagged(t *testing.T) {
	t.Parallel()
	src := `package p
// requestPlaneAsBuilt should stay commented: RunWithRuntime and runtimebundle.Build
import "github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
func ok() { _, _ = modelregistry.Build(nil, nil, nil) }
`
	got, err := scanRuntimeConvergenceSource("internal/core/modelregistry/synthetic_ok.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no false positives, got %v", got)
	}
}

func TestReloadContract_SyntheticThirdPackageDeclarationRejected(t *testing.T) {
	t.Parallel()
	src := `package extrareload
type TriggerKind string
type ResultCategory string
type ReloadTrigger struct{ Kind TriggerKind }
type ReloadResult struct{ Category ResultCategory }
type HistoryEntry struct{}
type ReloadStatus struct{}
const (
	TriggerSIGHUP TriggerKind = "sighup"
	ResultPublished ResultCategory = "published"
)
var AllResultCategories = []ResultCategory{ResultPublished}
`
	got, err := scanReloadContractSource("internal/extra/reload/model.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "type:TriggerKind") {
		t.Fatalf("expected TriggerKind declaration, got %v", got)
	}
	if !findingsContainIdentity(got, "type:ResultCategory") {
		t.Fatalf("expected ResultCategory declaration, got %v", got)
	}
}

func TestReloadContract_CanonicalPackageExempt(t *testing.T) {
	t.Parallel()
	src := `package configreload
type TriggerKind string
type ResultCategory string
type HistoryEntry struct{}
const TriggerAPI TriggerKind = "api"
var AllResultCategories = []ResultCategory{}
`
	got, err := scanReloadContractSource("pkg/lipsdk/configreload/contract.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("canonical package must be exempt, got %v", got)
	}
}

func TestReloadContract_TypeAliasAndSDKReexportExempt(t *testing.T) {
	t.Parallel()
	// Exact canonical aliases are supported only at the public facade path.
	// The internal-owner deletion gate separately forbids them under
	// internal/core/configreload; this global scanner still exempts exact
	// SDK aliases so lipruntime re-exports are not false positives.
	src := `package lipruntime
import sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
type TriggerKind = sdkreload.TriggerKind
type ResultCategory = sdkreload.ResultCategory
type Trigger = sdkreload.Trigger
type Result = sdkreload.Result
type Status = sdkreload.Status
type HistoryEntry = sdkreload.HistoryEntry
type ReloadTrigger = sdkreload.Trigger
type ReloadResult = sdkreload.Result
type ReloadStatus = sdkreload.Status
const TriggerAPI = sdkreload.TriggerAPI
var AllResultCategories = sdkreload.AllResultCategories
`
	got, err := scanReloadContractSource("pkg/lipruntime/reload_aliases.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("public facade aliases/re-exports must not be findings, got %v", got)
	}
}

func TestReloadContract_HermesReviewNonCanonicalReloadAliasesAreRejected(t *testing.T) {
	t.Parallel()
	// Hermes bypass: type alias to a non-canonical package previously skipped
	// every Assign form, concealing mirrored reload declarations.
	src := `package mirror
import legacy "example.com/legacy/reload"
type ReloadTrigger = legacy.ReloadTrigger
type ReloadResult = legacy.ReloadResult
type ReloadStatus = legacy.ReloadStatus
type TriggerKind = legacy.TriggerKind
type ResultCategory = legacy.ResultCategory
type HistoryEntry = legacy.HistoryEntry
`
	got, err := scanReloadContractSource("internal/extra/legacy_alias.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, id := range []string{
		"type:ReloadTrigger", "type:ReloadResult", "type:ReloadStatus",
		"type:TriggerKind", "type:ResultCategory", "type:HistoryEntry",
	} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("non-canonical aliases concealed mirrored reload declarations: missing %s, got %v", id, got)
		}
	}
}

func TestReloadContract_HermesReviewArbitraryReloadAliasesAreRejected(t *testing.T) {
	t.Parallel()
	// Hermes bypass: builtin/anonymous aliases previously skipped via Assign.
	src := `package mirror
type ReloadTrigger = string
type ReloadResult = struct{ Secret string }
type ReloadStatus = map[string]any
type TriggerKind = string
type ResultCategory = struct{}
type HistoryEntry = interface{ ID() int64 }
`
	got, err := scanReloadContractSource("internal/extra/arbitrary_alias.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, id := range []string{
		"type:ReloadTrigger", "type:ReloadResult", "type:ReloadStatus",
		"type:TriggerKind", "type:ResultCategory", "type:HistoryEntry",
	} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("arbitrary aliases concealed reload contract declarations: missing %s, got %v", id, got)
		}
	}
}

func TestReloadContract_CanonicalAliasWithRenamedImportExempt(t *testing.T) {
	t.Parallel()
	src := `package lipruntime
import canon "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
type TriggerKind = canon.TriggerKind
type ResultCategory = canon.ResultCategory
type Trigger = canon.Trigger
type Result = canon.Result
type Status = canon.Status
type HistoryEntry = canon.HistoryEntry
type ReloadTrigger = canon.Trigger
type ReloadResult = canon.Result
type ReloadStatus = canon.Status
`
	got, err := scanReloadContractSource("pkg/lipruntime/reload_aliases.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("canonical renamed-import aliases must be exempt, got %v", got)
	}
}

func TestReloadContract_WrongCanonicalTargetPairingRejected(t *testing.T) {
	t.Parallel()
	src := `package configreload
import sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
type ReloadStatus = sdkreload.Result
type ReloadTrigger = sdkreload.Status
type ReloadResult = sdkreload.Trigger
type TriggerKind = sdkreload.ResultCategory
type ResultCategory = sdkreload.TriggerKind
type HistoryEntry = sdkreload.Status
`
	got, err := scanReloadContractSource("internal/core/configreload/wrong_target.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, id := range []string{
		"type:ReloadStatus", "type:ReloadTrigger", "type:ReloadResult",
		"type:TriggerKind", "type:ResultCategory", "type:HistoryEntry",
	} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("wrong canonical target must be a finding: missing %s, got %v", id, got)
		}
	}
}

func TestReloadContract_IndirectLocalAliasChainRejected(t *testing.T) {
	t.Parallel()
	src := `package configreload
import sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
type Inner = sdkreload.Trigger
type ReloadTrigger = Inner
`
	got, err := scanReloadContractSource("internal/core/configreload/indirect.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "type:ReloadTrigger") {
		t.Fatalf("indirect local alias chain must remain a finding, got %v", got)
	}
}

func TestReloadContract_UnrelatedTypesOutsideVocabularyNotFlagged(t *testing.T) {
	t.Parallel()
	src := `package other
import sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
type MyWidget = string
type Handler struct{ Secret string }
type Result struct{ N int }
type Status struct{ OK bool }
type Trigger struct{}
type Cache = map[string]any
type NotReload = sdkreload.Result
const WidgetMax = 3
var Handlers []Handler
`
	got, err := scanReloadContractSource("internal/extra/unrelated.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("unrelated types outside reload vocabulary must not be findings, got %v", got)
	}
}

func TestReloadContract_HermesReviewNeutralDefinedTypesInConfigreloadRejected(t *testing.T) {
	t.Parallel()
	// Hermes bypass: omitting bare Trigger/Result/Status from the global name set
	// let noncanonical package configreload redefine the canonical short types.
	src := `package configreload
type Trigger struct{ Kind TriggerKind }
type Result struct{ Category ResultCategory }
type Status struct{ LastResult Result }
type TriggerKind string
type ResultCategory string
`
	got, err := scanReloadContractSource("internal/core/configreload/duplicate_neutral.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, id := range []string{"type:Trigger", "type:Result", "type:Status"} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("defined neutral reload types in package configreload must be findings: missing %s, got %v", id, got)
		}
	}
}

func TestReloadContract_HermesReviewNeutralWrongAliasesInConfigreloadRejected(t *testing.T) {
	t.Parallel()
	// Wrong/noncanonical/builtin aliases of the neutral LHS names.
	src := `package configreload
import (
	legacy "example.com/legacy/reload"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)
type Trigger = legacy.Trigger
type Result = sdkreload.Status
type Status = string
`
	got, err := scanReloadContractSource("internal/core/configreload/neutral_wrong_alias.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, id := range []string{"type:Trigger", "type:Result", "type:Status"} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("wrong/noncanonical/builtin neutral aliases must be findings: missing %s, got %v", id, got)
		}
	}

	// Anonymous + indirect aliases of neutral LHS names.
	src2 := `package configreload
import sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
type Trigger = struct{ Kind string }
type Inner = sdkreload.Result
type Result = Inner
type Status = map[string]any
`
	got2, err := scanReloadContractSource("internal/stdhttp/admin/configreload/neutral_anon.go", src2)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, id := range []string{"type:Trigger", "type:Result", "type:Status"} {
		if !findingsContainIdentity(got2, id) {
			t.Fatalf("anonymous/indirect neutral aliases must be findings: missing %s, got %v", id, got2)
		}
	}
}

func TestReloadContract_NeutralExactCanonicalAliasesInConfigreloadExempt(t *testing.T) {
	t.Parallel()
	src := `package configreload
import sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
type Trigger = sdkreload.Trigger
type Result = sdkreload.Result
type Status = sdkreload.Status
`
	got, err := scanReloadContractSource("internal/core/configreload/neutral_exact.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("exact direct neutral aliases to SDK canonical targets must be exempt, got %v", got)
	}
}

func TestReloadContract_NeutralNamesInUnrelatedPackagesNotFlagged(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		path string
		pkg  string
	}{
		{name: "jsonguard", path: "internal/core/jsonguard/result.go", pkg: "jsonguard"},
		{name: "routing", path: "internal/core/routing/status.go", pkg: "routing"},
		{name: "other", path: "internal/extra/other/trigger.go", pkg: "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := "package " + tc.pkg + "\n" +
				"type Trigger struct{}\n" +
				"type Result struct{ N int }\n" +
				"type Status struct{ OK bool }\n"
			got, err := scanReloadContractSource(tc.path, src)
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("unrelated package %s must not flag neutral names, got %v", tc.pkg, got)
			}
		})
	}
}

func TestReloadContract_NeutralNamesViaPathSegmentConfigreloadRejected(t *testing.T) {
	t.Parallel()
	// Path segment configreload alone is enough even when the package name differs.
	src := `package sneaky
type Trigger struct{}
type Result struct{}
type Status struct{}
`
	got, err := scanReloadContractSource("internal/extra/configreload/sneaky.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, id := range []string{"type:Trigger", "type:Result", "type:Status"} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("path-segment configreload must flag neutral types: missing %s, got %v", id, got)
		}
	}
}

func TestReloadContract_CanonicalOwnerNeutralTypesRemainExempt(t *testing.T) {
	t.Parallel()
	src := `package configreload
type Trigger struct{ Kind TriggerKind }
type Result struct{ Category ResultCategory }
type Status struct{ LastResult Result }
type TriggerKind string
type ResultCategory string
`
	got, err := scanReloadContractSource("pkg/lipsdk/configreload/contract.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("canonical owner path must remain exempt for neutral types, got %v", got)
	}
}

func TestReloadContract_SyntheticVocabularyExpansionRejected(t *testing.T) {
	t.Parallel()
	src := `package configreload
type ResultCategory string
const (
	ResultPublished ResultCategory = "published"
	ResultBrandNew  ResultCategory = "brand-new"
)
`
	got, err := scanReloadContractSource("internal/core/configreload/model.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "const:ResultBrandNew") {
		t.Fatalf("expected expanded vocabulary detection, got %v", got)
	}
}

func TestHostPath_SyntheticNewAttachReloadHostCallRejected(t *testing.T) {
	t.Parallel()
	src := `package cmd
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func extraServe() {
	_, _ = runtimebundle.AttachReloadHost(nil, runtimebundle.BootstrapResult{}, "", nil)
}
`
	got, err := scanHostPathSource("cmd/lipstd/synthetic_attach.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:extraServe->runtimebundle.AttachReloadHost#1") {
		t.Fatalf("expected AttachReloadHost call detection, got %v", got)
	}
}

func TestHostPath_SyntheticNewBuildBootstrapDeclarationRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func BuildBootstrap() {}
func AttachReloadHost() {}
func RunWithGenerationHost() {}
`
	got, err := scanHostPathSource("internal/infra/runtimebundle/synthetic_host.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, id := range []string{"func:BuildBootstrap", "func:AttachReloadHost"} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("expected %s, got %v", id, got)
		}
	}
}

func TestHostPath_UnrelatedBuildMethodNotFlagged(t *testing.T) {
	t.Parallel()
	src := `package modelregistry
func Build() {}
func (r *Registry) Build() {}
`
	got, err := scanHostPathSource("internal/core/modelregistry/synthetic_build.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if findingsContainIdentity(got, "func:Build") {
		t.Fatalf("unrelated Build must not be flagged, got %v", got)
	}
}

func TestConfigLoad_SyntheticNewStartupLoadOwnerRejected(t *testing.T) {
	t.Parallel()
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
func LoadBootstrapEffective() {}
func startup() { _, _ = config.LoadEffective(nil, nil, config.LoadEffectiveOptions{}) }
`
	got, err := scanConfigLoadSource("internal/infra/other/synthetic_load.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "func:LoadBootstrapEffective") {
		t.Fatalf("expected new startup owner detection, got %v", got)
	}
	if !findingsContainIdentity(got, "call:startup->config.LoadEffective#1") {
		t.Fatalf("expected direct startup LoadEffective detection, got %v", got)
	}
}

func TestConfigLoad_ReloadAttemptLoadEffectiveNotFlagged(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
import (
	"context"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)
func AttachReloadHost() {
	_ = func(ctx context.Context, raw []byte) (*config.EffectiveConfig, error) {
		return config.LoadEffective(ctx, raw, config.LoadEffectiveOptions{})
	}
}
`
	got, err := scanConfigLoadSource("internal/infra/runtimebundle/reload_host.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, f := range got {
		if strings.Contains(f.Identity, "LoadEffective") {
			t.Fatalf("reload-path LoadEffective must not count as startup load, got %v", got)
		}
	}
}

func findingsContainIdentity(fs []convergenceFinding, identity string) bool {
	for _, f := range fs {
		if f.Identity == identity {
			return true
		}
	}
	return false
}

func TestRuntimeConvergence_SyntheticFindingFailsAllowlistMatch(t *testing.T) {
	t.Parallel()
	allow := []convergenceAllowlistEntry{{
		Gate: gateRuntimeConvergence, Path: "internal/stdhttp/request_plane.go",
		Identity: "func:requestPlaneAsBuilt", Classification: classAdapter,
		RetirementTask: "3.5", Rationale: "baseline",
	}}
	findings := []convergenceFinding{
		{Gate: gateRuntimeConvergence, Path: "internal/stdhttp/request_plane.go",
			Identity: "func:requestPlaneAsBuilt", Classification: classAdapter},
		{Gate: gateRuntimeConvergence, Path: "cmd/lipstd/new.go",
			Identity: "call:newServe->stdhttp.RunWithRuntime#1", Classification: classCall,
			Detail: "synthetic new old-path caller"},
	}
	bad := convergenceAllowlistDrift(gateRuntimeConvergence, findings, allow)
	if len(bad) != 1 || !strings.Contains(bad[0], "call:newServe->stdhttp.RunWithRuntime#1") {
		t.Fatalf("expected synthetic new caller to fail allowlist match, got %v", bad)
	}
}

func TestRuntimeConvergence_StaleAllowlistEntryRejected(t *testing.T) {
	t.Parallel()
	allow := []convergenceAllowlistEntry{{
		Gate: gateRuntimeConvergence, Path: "internal/stdhttp/gone.go",
		Identity: "func:requestPlaneAsBuilt", Classification: classAdapter,
		RetirementTask: "3.5", Rationale: "removed code",
	}}
	bad := convergenceAllowlistDrift(gateRuntimeConvergence, nil, allow)
	if len(bad) != 1 || !strings.Contains(bad[0], "stale allowlist entry") {
		t.Fatalf("expected stale allowlist rejection, got %v", bad)
	}
}

// --- Adversarial fixtures (Hermes-reproduced bypasses + precision controls) ---

func TestRuntimeConvergence_DuplicateSameFunctionCallIsRejected(t *testing.T) {
	t.Parallel()
	// Positive: second identical forbidden call in the same function must get a
	// distinct site identity and fail one-to-one allowlist matching.
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func wire() {
	_, _ = runtimebundle.Build(nil, nil, nil, nil)
	_, _ = runtimebundle.Build(nil, nil, nil, nil)
}
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/dup_build.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:wire->runtimebundle.Build#1") {
		t.Fatalf("expected first site #1, got %v", got)
	}
	if !findingsContainIdentity(got, "call:wire->runtimebundle.Build#2") {
		t.Fatalf("expected second site #2, got %v", got)
	}
	allowOne := []convergenceAllowlistEntry{{
		Gate: gateRuntimeConvergence, Path: "internal/infra/other/dup_build.go",
		Identity: "call:wire->runtimebundle.Build#1", Classification: classCall,
		RetirementTask: "4.2", Rationale: "only first site allowlisted",
	}}
	bad := convergenceAllowlistDrift(gateRuntimeConvergence, got, allowOne)
	if len(bad) == 0 || !strings.Contains(strings.Join(bad, "\n"), "call:wire->runtimebundle.Build#2") {
		t.Fatalf("expected #2 to fail allowlist with only #1 present, got %v", bad)
	}
	allowBoth := []convergenceAllowlistEntry{
		{Gate: gateRuntimeConvergence, Path: "internal/infra/other/dup_build.go",
			Identity: "call:wire->runtimebundle.Build#1", Classification: classCall,
			RetirementTask: "4.2", Rationale: "site 1"},
		{Gate: gateRuntimeConvergence, Path: "internal/infra/other/dup_build.go",
			Identity: "call:wire->runtimebundle.Build#2", Classification: classCall,
			RetirementTask: "4.2", Rationale: "site 2"},
	}
	if drift := convergenceAllowlistDrift(gateRuntimeConvergence, got, allowBoth); len(drift) != 0 {
		t.Fatalf("exact two-site allowlist should pass, got %v", drift)
	}
}

func TestRuntimeConvergence_RuntimeFunctionAliasCallIsDetected(t *testing.T) {
	t.Parallel()
	// Hermes bypass: function-value alias of runtimebundle.Build.
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func wire() {
	legacy := runtimebundle.Build
	_, _ = legacy(nil, nil, nil, nil)
}
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/alias_build.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:wire->runtimebundle.Build#1") {
		t.Fatalf("expected aliased runtimebundle.Build call detection, got %v", got)
	}
}

func TestConfigLoad_ConfigLoadFunctionAliasCallIsDetected(t *testing.T) {
	t.Parallel()
	// Hermes bypass: function-value alias of config.LoadEffective.
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
func startup() {
	load := config.LoadEffective
	_, _ = load(nil, nil, config.LoadEffectiveOptions{})
}
`
	got, err := scanConfigLoadSource("internal/infra/other/alias_load.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:startup->config.LoadEffective#1") {
		t.Fatalf("expected aliased config.LoadEffective call detection, got %v", got)
	}
}

func TestRuntimeConvergence_AliasReassignedBeforeCallNotFlagged(t *testing.T) {
	t.Parallel()
	src := `package other
import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
)
func wire() {
	legacy := runtimebundle.Build
	legacy = modelregistry.Build
	_, _ = legacy(nil, nil, nil)
}
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/alias_reassign.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, f := range got {
		if strings.Contains(f.Identity, "runtimebundle.Build") {
			t.Fatalf("reassigned alias must not retain protected provenance, got %v", got)
		}
	}
}

func TestRuntimeConvergence_NestedShadowAliasNotFlaggedInsideShadow(t *testing.T) {
	t.Parallel()
	src := `package other
import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
)
func wire() {
	legacy := runtimebundle.Build
	{
		legacy := modelregistry.Build
		_, _ = legacy(nil, nil, nil)
	}
	_, _ = legacy(nil, nil, nil, nil)
}
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/alias_shadow.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	// Inner shadow call must not count; outer call after scope exit must count once.
	if !findingsContainIdentity(got, "call:wire->runtimebundle.Build#1") {
		t.Fatalf("expected outer alias call after shadow exit, got %v", got)
	}
	if findingsContainIdentity(got, "call:wire->runtimebundle.Build#2") {
		t.Fatalf("inner shadow must not produce a second protected call, got %v", got)
	}
	n := 0
	for _, f := range got {
		if strings.Contains(f.Identity, "runtimebundle.Build") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want exactly one protected Build call finding, got %d: %v", n, got)
	}
}

func TestRuntimeConvergence_UnrelatedPackageSameSpellingNotFlagged(t *testing.T) {
	t.Parallel()
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
func wire() {
	build := modelregistry.Build
	_, _ = build(nil, nil, nil)
}
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/unrelated_build.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("unrelated package Build must not be flagged, got %v", got)
	}
}

func TestRuntimeConvergence_AliasChainAndParenDetected(t *testing.T) {
	t.Parallel()
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func wire() {
	f := (runtimebundle.Build)
	g := f
	_, _ = g(nil, nil, nil, nil)
}
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/alias_chain.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:wire->runtimebundle.Build#1") {
		t.Fatalf("expected paren+chain alias detection, got %v", got)
	}
}

func TestRuntimeConvergence_VarAliasDetected(t *testing.T) {
	t.Parallel()
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func wire() {
	var legacy = runtimebundle.Build
	_, _ = legacy(nil, nil, nil, nil)
}
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/var_alias.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:wire->runtimebundle.Build#1") {
		t.Fatalf("expected var alias detection, got %v", got)
	}
}

func TestRuntimeConvergence_DotImportProtectedRejected(t *testing.T) {
	t.Parallel()
	// Parser-only fixture: valid import/call shape for scanner coverage.
	src := `package other
import . "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func wire() {
	_, _ = Build(nil, nil, nil, nil)
}
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/dot_build.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "dotimport:runtimebundle") {
		t.Fatalf("expected dot-import finding, got %v", got)
	}
	if !findingsContainIdentity(got, "call:wire->runtimebundle.Build#1") {
		t.Fatalf("expected unqualified Build via dot-import detection, got %v", got)
	}
}

func TestHostPath_FunctionAliasCallIsDetected(t *testing.T) {
	t.Parallel()
	src := `package cmd
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func extraServe() {
	attach := runtimebundle.AttachReloadHost
	_, _ = attach(nil, runtimebundle.BootstrapResult{}, "", nil)
}
`
	got, err := scanHostPathSource("cmd/lipstd/alias_attach.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:extraServe->runtimebundle.AttachReloadHost#1") {
		t.Fatalf("expected aliased AttachReloadHost detection, got %v", got)
	}
}

func TestRuntimeConvergence_DuplicateAllowlistEntryRejected(t *testing.T) {
	t.Parallel()
	entry := convergenceAllowlistEntry{
		Gate: gateRuntimeConvergence, Path: "internal/infra/other/dup_build.go",
		Identity: "call:wire->runtimebundle.Build#1", Classification: classCall,
		RetirementTask: "4.2", Rationale: "dup",
	}
	findings := []convergenceFinding{{
		Gate: gateRuntimeConvergence, Path: "internal/infra/other/dup_build.go",
		Identity: "call:wire->runtimebundle.Build#1", Classification: classCall,
	}}
	bad := convergenceAllowlistDrift(gateRuntimeConvergence, findings, []convergenceAllowlistEntry{entry, entry})
	if len(bad) == 0 || !strings.Contains(strings.Join(bad, "\n"), "duplicate allowlist entry") {
		t.Fatalf("expected duplicate allowlist rejection, got %v", bad)
	}
}

// --- Control-flow / closure soundness (Hermes review #3 bypasses) ---

func TestRuntimeConvergence_HermesReviewConditionalReassignmentCannotEraseProtectedAlias(t *testing.T) {
	t.Parallel()
	// Hermes bypass: conditional reassignment erased a protected alias that
	// remains reachable when cond=false.
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func wire(cond bool) {
	legacy := runtimebundle.Build
	if cond {
		legacy = safeBuild
	}
	_, _ = legacy(nil, nil, nil, nil)
}
func safeBuild(a, b, c, d any) (any, error) { return nil, nil }
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/cond_reassign.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:wire->runtimebundle.Build#1") {
		t.Fatalf("conditional reassignment must not erase protected alias when cond=false, got %v", got)
	}
}

func TestRuntimeConvergence_HermesReviewUninvokedClosureCannotEraseProtectedAlias(t *testing.T) {
	t.Parallel()
	// Hermes bypass: uninvoked closure reassignment erased the outer protected alias.
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func wire() {
	legacy := runtimebundle.Build
	_ = func() { legacy = safeBuild }
	_, _ = legacy(nil, nil, nil, nil)
}
func safeBuild(a, b, c, d any) (any, error) { return nil, nil }
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/closure_reassign.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:wire->runtimebundle.Build#1") {
		t.Fatalf("uninvoked closure must not erase outer protected alias, got %v", got)
	}
}

func TestRuntimeConvergence_IfElseEitherBranchPreservesProtectedAlias(t *testing.T) {
	t.Parallel()
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func wire(cond bool) {
	var legacy func(a, b, c, d any) (any, error)
	if cond {
		legacy = runtimebundle.Build
	} else {
		legacy = safeBuild
	}
	_, _ = legacy(nil, nil, nil, nil)
}
func safeBuild(a, b, c, d any) (any, error) { return nil, nil }
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/ifelse_either.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:wire->runtimebundle.Build#1") {
		t.Fatalf("if/else must keep protected provenance when either branch provides it, got %v", got)
	}
}

func TestRuntimeConvergence_SwitchBranchIsolationPreservesProtectedAlias(t *testing.T) {
	t.Parallel()
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func wire(n int) {
	legacy := runtimebundle.Build
	switch n {
	case 1:
		legacy = safeBuild
	case 2:
		legacy = otherSafe
	}
	_, _ = legacy(nil, nil, nil, nil)
}
func safeBuild(a, b, c, d any) (any, error) { return nil, nil }
func otherSafe(a, b, c, d any) (any, error) { return nil, nil }
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/switch_isolate.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:wire->runtimebundle.Build#1") {
		t.Fatalf("switch case reassignment must not erase protected alias on other paths, got %v", got)
	}
}

func TestRuntimeConvergence_SelectBranchIsolationPreservesProtectedAlias(t *testing.T) {
	t.Parallel()
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func wire(ch chan int) {
	legacy := runtimebundle.Build
	select {
	case <-ch:
		legacy = safeBuild
	default:
	}
	_, _ = legacy(nil, nil, nil, nil)
}
func safeBuild(a, b, c, d any) (any, error) { return nil, nil }
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/select_isolate.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:wire->runtimebundle.Build#1") {
		t.Fatalf("select branch reassignment must not erase protected alias on other paths, got %v", got)
	}
}

func TestRuntimeConvergence_ZeroIterationLoopPreservesProtectedAlias(t *testing.T) {
	t.Parallel()
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func wire(xs []int) {
	legacy := runtimebundle.Build
	for range xs {
		legacy = safeBuild
	}
	_, _ = legacy(nil, nil, nil, nil)
}
func safeBuild(a, b, c, d any) (any, error) { return nil, nil }
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/loop_zero.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:wire->runtimebundle.Build#1") {
		t.Fatalf("zero-iteration loop must preserve protected alias, got %v", got)
	}
}

func TestRuntimeConvergence_SimultaneousSwapTracksProtectedProvenance(t *testing.T) {
	t.Parallel()
	// Shared function type keeps the fixture a compiling shape at the AST level.
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
type buildFn func(a, b, c, d any) (any, error)
func safeBuild(a, b, c, d any) (any, error) { return nil, nil }
func wire() {
	var legacy, other buildFn
	legacy = runtimebundle.Build
	other = safeBuild
	legacy, other = other, legacy
	_, _ = other(nil, nil, nil, nil)
}
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/swap_alias.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:wire->runtimebundle.Build#1") {
		t.Fatalf("simultaneous swap must track protected provenance onto other, got %v", got)
	}
}

func TestRuntimeConvergence_AllBranchesKillProtectedAliasNotFlagged(t *testing.T) {
	t.Parallel()
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func wire(cond bool) {
	legacy := runtimebundle.Build
	if cond {
		legacy = safeBuild
	} else {
		legacy = otherSafe
	}
	_, _ = legacy(nil, nil, nil, nil)
}
func safeBuild(a, b, c, d any) (any, error) { return nil, nil }
func otherSafe(a, b, c, d any) (any, error) { return nil, nil }
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/all_branches_kill.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, f := range got {
		if strings.Contains(f.Identity, "runtimebundle.Build") {
			t.Fatalf("all-reachable-branch kill must not flag protected alias, got %v", got)
		}
	}
}

func TestRuntimeConvergence_ClosureBodyProtectedCallStillDetected(t *testing.T) {
	t.Parallel()
	src := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func wire() {
	legacy := runtimebundle.Build
	_ = func() {
		_, _ = legacy(nil, nil, nil, nil)
	}
}
`
	got, err := scanRuntimeConvergenceSource("internal/infra/other/closure_call.go", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !findingsContainIdentity(got, "call:wire->runtimebundle.Build#1") {
		t.Fatalf("protected call inside closure body must still be detected, got %v", got)
	}
}
