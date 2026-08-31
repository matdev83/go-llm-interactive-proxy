package archtest

import (
	"strings"
	"testing"
)

// TestForbiddenMirrorPredicate_FeatureBundleField verifies that hand-authored
// plane fields in FeatureBundle are rejected when their wave is completed,
// while whitelisted non-plane fields and generated files pass.
func TestForbiddenMirrorPredicate_FeatureBundleField(t *testing.T) {
	t.Parallel()

	forbiddenSrc := `package feature
import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"

type FeatureBundle struct {
	SchemaVersion int
	SubmitHooks   []hooks.SubmitHook
}
`
	findings := scanSyntheticSource(t, "pkg/lipsdk/feature/bundle.go", forbiddenSrc, Wave5c_Residual)
	if len(findings) == 0 {
		t.Fatalf("expected forbidden FeatureBundle field finding for SubmitHooks at Wave5c")
	}
	if findings[0].ShapeKind != MirrorFeatureBundleField || findings[0].PlaneID != "submit_hooks" {
		t.Fatalf("unexpected finding: %+v", findings[0])
	}

	// At Wave1 (and Wave0), FeatureBundle SDK contract fields are allowed until Wave5c residual removal
	findingsW1 := scanSyntheticSource(t, "pkg/lipsdk/feature/bundle.go", forbiddenSrc, Wave1_HookBus)
	if len(findingsW1) != 0 {
		t.Fatalf("expected 0 findings at Wave1 for FeatureBundle SDK field, got %+v", findingsW1)
	}

	// Whitelisted fields (SchemaVersion, Lifecycles, contributions) pass at all waves
	allowedSrc := `package feature
import lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"

type FeatureBundle struct {
	SchemaVersion int
	Lifecycles    []lipplugin.Lifecycle
	contributions ContributionSet
}
`
	allowedFindings := scanSyntheticSource(t, "pkg/lipsdk/feature/bundle.go", allowedSrc, Wave5c_Residual)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for allowed FeatureBundle fields, got %+v", allowedFindings)
	}
}

// TestForbiddenMirrorPredicate_MergedFeatureSurfaceField verifies that hand-authored
// plane fields in MergedFeatureSurface are rejected when their wave is completed.
func TestForbiddenMirrorPredicate_MergedFeatureSurfaceField(t *testing.T) {
	t.Parallel()

	forbiddenSrc := `package featurebundle
import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"

type MergedFeatureSurface struct {
	RequestTransforms []request.Transform
}
`
	findings := scanSyntheticSource(t, "internal/featurebundle/merge_surface.go", forbiddenSrc, Wave3_RequestShaping)
	if len(findings) == 0 {
		t.Fatalf("expected forbidden MergedFeatureSurface field finding for RequestTransforms at Wave3")
	}
	if findings[0].ShapeKind != MirrorMergedSurfaceField || findings[0].PlaneID != "request_transforms" {
		t.Fatalf("unexpected finding: %+v", findings[0])
	}

	// Whitelisted fields pass
	allowedSrc := `package featurebundle
import (
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

type MergedFeatureSurface struct {
	Lifecycles []lipplugin.Lifecycle
	frozen     lipfeature.FrozenPlaneSet
}
`
	allowedFindings := scanSyntheticSource(t, "internal/featurebundle/merge_surface.go", allowedSrc, Wave5c_Residual)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for allowed MergedFeatureSurface fields, got %+v", allowedFindings)
	}
}

// TestForbiddenMirrorPredicate_AppendBranch verifies that per-plane append branches
// in MergedFeatureSurface.Append are rejected.
func TestForbiddenMirrorPredicate_AppendBranch(t *testing.T) {
	t.Parallel()

	forbiddenSrc := `package featurebundle
import lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"

func (m *MergedFeatureSurface) Append(b lipfeature.FeatureBundle) error {
	m.SubmitHooks = append(m.SubmitHooks, b.SubmitHooks...)
	return nil
}
`
	findings := scanSyntheticSource(t, "internal/featurebundle/merge_surface.go", forbiddenSrc, Wave1_HookBus)
	if len(findings) == 0 {
		t.Fatalf("expected forbidden AppendBranch finding for SubmitHooks at Wave1")
	}
	if findings[0].ShapeKind != MirrorAppendBranch || findings[0].PlaneID != "submit_hooks" {
		t.Fatalf("unexpected finding: %+v", findings[0])
	}

	// Lifecycle appending is allowed
	allowedSrc := `package featurebundle
import lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"

func (m *MergedFeatureSurface) Append(b lipfeature.FeatureBundle) error {
	m.Lifecycles = append(m.Lifecycles, b.Lifecycles...)
	return nil
}
`
	allowedFindings := scanSyntheticSource(t, "internal/featurebundle/merge_surface.go", allowedSrc, Wave5c_Residual)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for allowed Lifecycle append, got %+v", allowedFindings)
	}
}

// TestForbiddenMirrorPredicate_ProjectionBranches verifies that per-plane field
// projections in extensionsFromMerged, overlayExtensions, and hooksConfigFromMerged are rejected.
func TestForbiddenMirrorPredicate_ProjectionBranches(t *testing.T) {
	t.Parallel()

	// 1. extensionsFromMerged
	extSrc := `package runtimebundle
import "github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"

func extensionsFromMerged(merged featurebundle.MergedFeatureSurface, processOpts *BuildOptions) ExtensionsOptions {
	return ExtensionsOptions{
		SessionOpeners: append(merged.SessionOpeners[:0:0], merged.SessionOpeners...),
	}
}
`
	findingsExt := scanSyntheticSource(t, "internal/infra/runtimebundle/compile_generation.go", extSrc, Wave3_RequestShaping)
	if len(findingsExt) == 0 {
		t.Fatalf("expected forbidden ProjectionBranch finding for SessionOpeners in extensionsFromMerged")
	}
	if findingsExt[0].ShapeKind != MirrorProjectionBranch || findingsExt[0].PlaneID != "session_openers" {
		t.Fatalf("unexpected finding: %+v", findingsExt[0])
	}

	// 2. overlayExtensions
	overlaySrc := `package runtimebundle

func overlayExtensions(dst *ExtensionsOptions, src ExtensionsOptions) {
	dst.ToolCatalogFilters = append(dst.ToolCatalogFilters, src.ToolCatalogFilters...)
}
`
	findingsOverlay := scanSyntheticSource(t, "internal/infra/runtimebundle/compile_generation.go", overlaySrc, Wave4_Tools)
	if len(findingsOverlay) == 0 {
		t.Fatalf("expected forbidden ProjectionBranch finding for ToolCatalogFilters in overlayExtensions")
	}
	if findingsOverlay[0].ShapeKind != MirrorProjectionBranch || findingsOverlay[0].PlaneID != "tool_catalog_filters" {
		t.Fatalf("unexpected finding: %+v", findingsOverlay[0])
	}

	// 3. hooksConfigFromMerged
	hooksSrc := `package runtimebundle
import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
)

func hooksConfigFromMerged(m featurebundle.MergedFeatureSurface) hooks.Config {
	return hooks.Config{
		SubmitHooks: m.SubmitHooks,
	}
}
`
	findingsHooks := scanSyntheticSource(t, "internal/infra/runtimebundle/build_feature_hooks.go", hooksSrc, Wave1_HookBus)
	if len(findingsHooks) == 0 {
		t.Fatalf("expected forbidden ProjectionBranch finding for SubmitHooks in hooksConfigFromMerged")
	}
	if findingsHooks[0].ShapeKind != MirrorProjectionBranch || findingsHooks[0].PlaneID != "submit_hooks" {
		t.Fatalf("unexpected finding: %+v", findingsHooks[0])
	}
}

// TestForbiddenMirrorPredicate_ExtensionsOptionsField verifies that hand-authored
// plane fields in ExtensionsOptions are rejected.
func TestForbiddenMirrorPredicate_ExtensionsOptionsField(t *testing.T) {
	t.Parallel()

	forbiddenSrc := `package runtimebundle
import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"

type ExtensionsOptions struct {
	TrafficObservers []traffic.Observer
}
`
	findings := scanSyntheticSource(t, "internal/infra/runtimebundle/options.go", forbiddenSrc, Wave2_Observers)
	if len(findings) == 0 {
		t.Fatalf("expected forbidden ExtensionsOptions field finding for TrafficObservers at Wave2")
	}
	if findings[0].ShapeKind != MirrorExtensionsOptionsField || findings[0].PlaneID != "traffic_observers" {
		t.Fatalf("unexpected finding: %+v", findings[0])
	}

	// Whitelisted host capability fields pass
	allowedSrc := `package runtimebundle
import (
	coresg "github.com/matdev83/go-llm-interactive-proxy/internal/core/secretguard"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

type ExtensionsOptions struct {
	SecretGuardInputs      SecretGuardInputs
	SecretGuardEnvironment coresg.Environment
	SecretDecisionObserver sdk.Observer
	frozen                 lipfeature.FrozenPlaneSet
}
`
	allowedFindings := scanSyntheticSource(t, "internal/infra/runtimebundle/options.go", allowedSrc, Wave5c_Residual)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for allowed ExtensionsOptions fields, got %+v", allowedFindings)
	}
}

// TestForbiddenMirrorPredicate_GenerationOperations verifies that generation operation
// fields and accessors not delegating to Get are rejected, while thin delegates pass.
func TestForbiddenMirrorPredicate_GenerationOperations(t *testing.T) {
	t.Parallel()

	// Direct struct field on generationOperations
	forbiddenFieldSrc := `package runtimebundle
import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"

type generationOperations struct {
	terminalDecisionProvider terminaldecision.Provider
}
`
	findingsField := scanSyntheticSource(t, "internal/infra/runtimebundle/generation_bundle.go", forbiddenFieldSrc, Wave5b_LocalTurnTerminal)
	if len(findingsField) == 0 {
		t.Fatalf("expected forbidden generationOperations field finding for terminalDecisionProvider at Wave5b")
	}
	if findingsField[0].ShapeKind != MirrorGenerationOpField || findingsField[0].PlaneID != "terminal_decision_provider" {
		t.Fatalf("unexpected finding: %+v", findingsField[0])
	}

	// Non-delegating accessor method
	nonDelegatingSrc := `package runtimebundle
import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"

func (b *GenerationBundle) TerminalDecisionProvider() terminaldecision.Provider {
	return b.operations.terminalDecisionProvider
}
`
	findingsMethod := scanSyntheticSource(t, "internal/infra/runtimebundle/generation_bundle.go", nonDelegatingSrc, Wave5b_LocalTurnTerminal)
	if len(findingsMethod) == 0 {
		t.Fatalf("expected forbidden non-delegating method finding for TerminalDecisionProvider at Wave5b")
	}
	if findingsMethod[0].ShapeKind != MirrorGenerationOpField {
		t.Fatalf("unexpected finding: %+v", findingsMethod[0])
	}

	// Thin delegate calling lipfeature.Get passes
	thinDelegateSrc := `package runtimebundle
import (
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

func (b *GenerationBundle) TerminalDecisionProvider() terminaldecision.Provider {
	return lipfeature.Get(b.operations.frozen, lipfeature.PlaneTerminalDecisionProvider)
}
`
	allowedFindings := scanSyntheticSource(t, "internal/infra/runtimebundle/generation_bundle.go", thinDelegateSrc, Wave5c_Residual)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for thin delegate method, got %+v", allowedFindings)
	}

	// Nil-safe thin delegate calling lipfeature.Get passes
	nilSafeThinDelegateSrc := `package runtimebundle
import (
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

func (b *GenerationBundle) TerminalDecisionProvider() terminaldecision.Provider {
	if b == nil {
		return nil
	}
	return lipfeature.Get(b.operations.frozen, lipfeature.PlaneTerminalDecisionProvider)
}
`
	allowedNilSafeFindings := scanSyntheticSource(t, "internal/infra/runtimebundle/generation_bundle.go", nilSafeThinDelegateSrc, Wave5c_Residual)
	if len(allowedNilSafeFindings) != 0 {
		t.Fatalf("expected 0 findings for nil-safe thin delegate method, got %+v", allowedNilSafeFindings)
	}
}

// TestForbiddenMirrorPredicate_GeneratedFileWhitelist verifies that generated files
// are completely whitelisted from mirror checks.
func TestForbiddenMirrorPredicate_GeneratedFileWhitelist(t *testing.T) {
	t.Parallel()

	generatedSrc := `// Code generated by generate-feature-planes.go. DO NOT EDIT.
package feature
import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"

type FeatureBundle struct {
	SubmitHooks []hooks.SubmitHook
}
`
	findings := scanSyntheticSource(t, "pkg/lipsdk/feature/plane_generated.go", generatedSrc, Wave5c_Residual)
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings for generated file, got %+v", findings)
	}
}

// TestForbiddenMirrorPredicate_HookProjectionsHandwrittenAndSpoofingRejected verifies that
// adversarial and handwritten hook projections (including former allowlist symbols
// HooksConfigFromGenerated and HooksConfigFromFrozen) reading hook planes via struct
// fields or Get are strictly rejected under Wave1.
func TestForbiddenMirrorPredicate_HookProjectionsHandwrittenAndSpoofingRejected(t *testing.T) {
	t.Parallel()

	// 1. Adversarial HooksConfigFromMerged reading MergedFeatureSurface.SubmitHooks is REJECTED
	adversarialUpperSrc := `package runtimebundle
import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
)

func HooksConfigFromMerged(m featurebundle.MergedFeatureSurface) hooks.Config {
	return hooks.Config{
		SubmitHooks: m.SubmitHooks,
	}
}
`
	findingsUpper := scanSyntheticSource(t, "internal/infra/runtimebundle/build_feature_hooks.go", adversarialUpperSrc, Wave1_HookBus)
	if len(findingsUpper) == 0 {
		t.Fatalf("expected forbidden finding for uppercase adversarial HooksConfigFromMerged at Wave1")
	}
	if findingsUpper[0].ShapeKind != MirrorProjectionBranch || findingsUpper[0].PlaneID != "submit_hooks" {
		t.Fatalf("unexpected finding for HooksConfigFromMerged: %+v", findingsUpper[0])
	}

	// 2. Spoofed variants HooksConfigFromMerged2 and HooksConfigfromX are REJECTED
	spoofVariants := []struct {
		name     string
		funcName string
	}{
		{name: "numbered spoof", funcName: "HooksConfigFromMerged2"},
		{name: "casing spoof", funcName: "HooksConfigfromX"},
	}

	for _, tc := range spoofVariants {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := `package runtimebundle
import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
)

func ` + tc.funcName + `(m featurebundle.MergedFeatureSurface) hooks.Config {
	return hooks.Config{
		SubmitHooks: m.SubmitHooks,
	}
}
`
			findings := scanSyntheticSource(t, "internal/infra/runtimebundle/build_feature_hooks.go", src, Wave1_HookBus)
			if len(findings) == 0 {
				t.Fatalf("expected forbidden finding for spoofed %s (%s) at Wave1", tc.name, tc.funcName)
			}
			if findings[0].ShapeKind != MirrorProjectionBranch || findings[0].PlaneID != "submit_hooks" {
				t.Fatalf("unexpected finding for %s: %+v", tc.funcName, findings[0])
			}
		})
	}

	// 3. Former allowlist functions (HooksConfigFromGenerated, HooksConfigFromFrozen) with handwritten Get are REJECTED
	formerAllowedHookSrc := `package runtimebundle
import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func HooksConfigFromGenerated(g featurebundle.GeneratedMergeSurface, p sdkhooks.ToolReactorErrorPolicy) hooks.Config {
	return HooksConfigFromFrozen(g.Frozen, p)
}

func HooksConfigFromFrozen(f lipfeature.FrozenPlaneSet, p sdkhooks.ToolReactorErrorPolicy) hooks.Config {
	return hooks.Config{
		SubmitHooks: lipfeature.Get(f, lipfeature.PlaneSubmitHooks),
	}
}
`
	rejectedFindings := scanSyntheticSource(t, "internal/infra/runtimebundle/build_feature_hooks.go", formerAllowedHookSrc, Wave1_HookBus)
	if len(rejectedFindings) == 0 {
		t.Fatalf("expected forbidden finding for handwritten HooksConfigFromFrozen at Wave1 when no allowlist exists")
	}
	if rejectedFindings[0].ShapeKind != MirrorProjectionBranch || rejectedFindings[0].PlaneID != "submit_hooks" {
		t.Fatalf("unexpected finding for former allowed hook projection: %+v", rejectedFindings[0])
	}
}

// TestForbiddenMirrorsAbsent_ProductionBaseline verifies that at the current repository
// baseline wave (ActiveMigrationWave = Wave0_Baseline), the production repository has zero
// forbidden mirror violations.
func TestForbiddenMirrorsAbsent_ProductionBaseline(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	findings, err := ScanForbiddenMirrors(root, ActiveMigrationWave)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		var b strings.Builder
		for _, f := range findings {
			b.WriteString(f.String())
			b.WriteByte('\n')
		}
		t.Fatalf("forbidden mirrors present at active wave (%d):\n%s", len(findings), b.String())
	}
}
