package archtest

// Negative fixtures for the ALG strategy-isolation ratchets: verifier
// unreachability from preferred strategy construction (design ratchet 7) and
// control-tool-provider unreachability from legacy strategy construction
// (design ratchet 8).
//
// Both fixtures build a miniature same-package overlay, exactly as
// request_attempt_state_baseline_test.go overlays a temporary AST, so the
// reachability walk is exercised on a real call graph rather than asserted from
// a grep. See agent_loop_guard_ownership_fixture_test.go for the shared helpers.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTask121FixturePreferredStrategyRejectsVerifierReach proves design ratchet 7
// fires when the preferred construction graph can reach the verifier, whether
// directly in the receiver's own file, one hop away, or two hops away, and that
// a verifier-free preferred graph passes even while the legacy receiver in the
// same package uses one.
func TestTask121FixturePreferredStrategyRejectsVerifierReach(t *testing.T) {
	t.Parallel()
	spec := AlgPreferredStrategyIsolationSpec()

	t.Run("positive verifier-free preferred graph passes beside a legacy verifier user", func(t *testing.T) {
		t.Parallel()
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile:    algPreferredOverlayProvider,
			algPreferredProviderFile: algPreferredOverlayReceiver,
			algFeatureRootDir + "/verifier_bridge.go": `package agentloopguard

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard/verifier"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

func (p provider) consult(ctx context.Context, in terminaldecision.Input) error {
	_, err := verifier.New(nil, verifier.Config{}).Verify(ctx, in)
	return err
}
`,
		})
		assert.Empty(t, algFixtureRender(ScanAgentLoopGuardStrategyReachability(files, spec)))
	})

	t.Run("receiver file imports the verifier", func(t *testing.T) {
		t.Parallel()
		src := strings.Replace(algPreferredOverlayReceiver,
			`"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"`,
			`"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard/verifier"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"`, 1)
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile:    algPreferredOverlayProvider,
			algPreferredProviderFile: src,
		})
		findings := ScanAgentLoopGuardStrategyReachability(files, spec)
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGPreferredStrategyIsolation,
			"which imports the forbidden dependency"),
			"preferred isolation ratchet must reject a verifier import in the receiver file:\n%s", algFixtureRender(findings))
	})

	t.Run("one hop helper calls the verifier", func(t *testing.T) {
		t.Parallel()
		src := strings.Replace(algPreferredOverlayReceiver,
			"limit := p.limits()",
			"if err := p.revalidate(ctx, in); err != nil {\n\t\treturn allowStop(\"protocol_inactive\"), nil\n\t}\n\tlimit := p.limits()", 1) +
			"\nfunc (p preferredProvider) revalidate(ctx context.Context, in terminaldecision.Input) error {\n\treturn p.consult(ctx, in)\n}\n"
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile:    algPreferredOverlayProvider,
			algPreferredProviderFile: src,
			algFeatureRootDir + "/verifier_bridge.go": `package agentloopguard

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard/verifier"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

func (p preferredProvider) consult(ctx context.Context, in terminaldecision.Input) error {
	_, err := verifier.New(nil, verifier.Config{}).Verify(ctx, in)
	return err
}
`,
		})
		findings := ScanAgentLoopGuardStrategyReachability(files, spec)
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGPreferredStrategyIsolation,
			`calls verifier.New from the forbidden dependency`),
			"preferred isolation ratchet must follow one hop into the verifier:\n%s", algFixtureRender(findings))
	})

	t.Run("two hops through a plain package function", func(t *testing.T) {
		t.Parallel()
		src := strings.Replace(algPreferredOverlayReceiver,
			"limit := p.limits()",
			"evaluateProtocol(ctx, in)\n\tlimit := p.limits()", 1) +
			"\nfunc evaluateProtocol(ctx context.Context, in terminaldecision.Input) { checkSemantics(ctx, in) }\n" +
			"\nfunc checkSemantics(ctx context.Context, in terminaldecision.Input) { consultVerifier(ctx, in) }\n"
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile:    algPreferredOverlayProvider,
			algPreferredProviderFile: src,
			algFeatureRootDir + "/verifier_bridge.go": `package agentloopguard

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard/verifier"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

func consultVerifier(ctx context.Context, in terminaldecision.Input) error {
	_, err := verifier.New(nil, verifier.Config{}).Verify(ctx, in)
	return err
}
`,
		})
		findings := ScanAgentLoopGuardStrategyReachability(files, spec)
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGPreferredStrategyIsolation,
			`calls verifier.New from the forbidden dependency`),
			"preferred isolation ratchet must follow plain-function hops into the verifier:\n%s", algFixtureRender(findings))
	})

	t.Run("missing strategy case is unprovable, not silently skipped", func(t *testing.T) {
		t.Parallel()
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile: `package agentloopguard

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"

func NewConfiguredProvider(cfg Config) (terminaldecision.Provider, error) {
	return provider{}, nil
}

type provider struct{}

func (provider) ID() string { return providerID }

func (p provider) Decide(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	return allowStop("legacy"), nil
}

func allowStop(reason string) terminaldecision.Decision { return terminaldecision.Decision{ReasonCode: reason} }
`,
			algPreferredProviderFile: algPreferredOverlayReceiver,
		})
		findings := ScanAgentLoopGuardStrategyReachability(files, spec)
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGPreferredStrategyIsolation,
			"has no StrategyAttemptCompletion case"),
			"preferred isolation ratchet must fail closed when the strategy branch is gone:\n%s", algFixtureRender(findings))
	})
}

// TestTask121FixtureStrategyWalkDocumentedCoverage pins the walk's APPROXIMATION
// as deliberate, so a future reader cannot mistake it for full reachability, and
// so nobody "fixes" it into a fail-closed variant that misreports the approved
// live layout.
//
// The covered set is the strategy case body, the receiver's own methods, and the
// plain-function chain they reach. A method reached through a RECEIVER VARIABLE
// is a reference the walk cannot attribute without type information, so it stops
// there; the receiver's methods are in scope by wholesale seeding instead.
// Failing closed on that gap is not the fix: the shared allowStop helper in
// provider.go pulls the legacy receiver's file into the preferred strategy's
// reached set, and that file imports the verifier legitimately, so a strict
// variant reports the shipped layout as a violation.
func TestTask121FixtureStrategyWalkDocumentedCoverage(t *testing.T) {
	t.Parallel()
	spec := AlgPreferredStrategyIsolationSpec()

	// algWalkBridgeFile is a verifier-calling method on the preferred receiver.
	// The walk must reach it through the receiver's seeded method set.
	const algWalkBridgeFile = `package agentloopguard

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard/verifier"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

func (preferredProvider) consult(ctx context.Context, in terminaldecision.Input) error {
	_, err := verifier.New(nil, verifier.Config{}).Verify(ctx, in)
	return err
}
`

	t.Run("a verifier call in the receiver's own method is reached", func(t *testing.T) {
		t.Parallel()
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile:                     algPreferredOverlayProvider,
			algPreferredProviderFile:                  algPreferredOverlayReceiver,
			algFeatureRootDir + "/verifier_bridge.go": algWalkBridgeFile,
		})
		findings := ScanAgentLoopGuardStrategyReachability(files, spec)
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGPreferredStrategyIsolation,
			`calls verifier.New from the forbidden dependency`),
			"the receiver's own methods are in scope by seeding, so this shape must fire:\n%s", algFixtureRender(findings))
	})

	t.Run("a method reached only through a receiver variable is not claimed", func(t *testing.T) {
		t.Parallel()
		src := strings.Replace(algPreferredOverlayReceiver,
			"limit := p.limits()",
			"limit := p.limits()\n\tif err := p.indirect(ctx, in); err != nil {\n\t\treturn allowStop(\"protocol_inactive\"), nil\n\t}", 1) +
			"\nfunc (p preferredProvider) indirect(ctx context.Context, in terminaldecision.Input) error {\n\thelper := p\n\treturn helper.consult(ctx, in)\n}\n"
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile:                     algPreferredOverlayProvider,
			algPreferredProviderFile:                  src,
			algFeatureRootDir + "/verifier_bridge.go": algWalkBridgeFile,
		})
		findings := ScanAgentLoopGuardStrategyReachability(files, spec)
		for _, finding := range findings {
			assert.NotContains(t, finding.Detail, "indirect",
				"p.indirect IS a seeded receiver method, but only its receiver-variable "+
					"call to consult is unfollowed; the walk must not claim it followed that hop")
		}
	})
}

// algLegacyOverlayReceiver is a miniature approved legacy receiver: it uses the
// legacy verifier and never touches the proxy-owned control tool.
const algLegacyOverlayReceiver = `package agentloopguard

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

type provider struct{}

func (provider) ID() string { return providerID }

func (p provider) Decide(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	if !decodeProgressState(in) {
		return allowStop("invalid_progress_state"), nil
	}
	return allowStop("unfinished_objective"), nil
}

func decodeProgressState(in terminaldecision.Input) bool { return true }

func allowStop(reason string) terminaldecision.Decision { return terminaldecision.Decision{ReasonCode: reason} }
`

// TestTask121FixtureLegacyStrategyRejectsControlToolReach proves design ratchet 8
// fires when the legacy construction graph can reach the control-tool provider,
// through the SDK package or through the feature-local constructor, directly or
// through a hop, and that a control-tool-free legacy graph passes beside a
// preferred receiver in the same package that owns the tool.
func TestTask121FixtureLegacyStrategyRejectsControlToolReach(t *testing.T) {
	t.Parallel()
	spec := AlgLegacyStrategyIsolationSpec()

	t.Run("positive control-tool-free legacy graph passes beside a control-tool owner", func(t *testing.T) {
		t.Parallel()
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile:    algLegacyOverlayReceiver + "\n" + algPreferredOverlayConstructor,
			algPreferredProviderFile: algPreferredOverlayReceiver,
			algFeatureRootDir + "/completiontool_handle.go": `package agentloopguard

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controltool"

func NewCompletionToolProvider() controltool.Provider { return nil }
`,
		})
		assert.Empty(t, algFixtureRender(ScanAgentLoopGuardStrategyReachability(files, spec)))
	})

	t.Run("legacy receiver calls the control-tool SDK", func(t *testing.T) {
		t.Parallel()
		src := strings.Replace(algLegacyOverlayReceiver,
			"if !decodeProgressState(in) {",
			"controltool.ValidateOutcome(terminaldecision.Decision{})\n\tif !decodeProgressState(in) {", 1)
		src = strings.Replace(src,
			`"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"`,
			`"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controltool"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"`, 1)
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile:    src + "\n" + algPreferredOverlayConstructor,
			algPreferredProviderFile: algPreferredOverlayReceiver,
		})
		findings := ScanAgentLoopGuardStrategyReachability(files, spec)
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGLegacyStrategyIsolation,
			`calls controltool.ValidateOutcome from the forbidden dependency`),
			"legacy isolation ratchet must reject a direct control-tool call:\n%s", algFixtureRender(findings))
	})

	t.Run("legacy receiver constructs the control-tool provider locally", func(t *testing.T) {
		t.Parallel()
		src := strings.Replace(algLegacyOverlayReceiver,
			"if !decodeProgressState(in) {",
			"_ = NewCompletionToolProvider()\n\tif !decodeProgressState(in) {", 1)
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile:    src + "\n" + algPreferredOverlayConstructor,
			algPreferredProviderFile: algPreferredOverlayReceiver,
			algFeatureRootDir + "/completiontool_handle.go": `package agentloopguard

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controltool"

func NewCompletionToolProvider() controltool.Provider { return nil }
`,
		})
		findings := ScanAgentLoopGuardStrategyReachability(files, spec)
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGLegacyStrategyIsolation,
			`reaches "NewCompletionToolProvider"`),
			"legacy isolation ratchet must reject a same-package control-tool entry point:\n%s", algFixtureRender(findings))
	})

	t.Run("legacy receiver reaches the control tool through a hop", func(t *testing.T) {
		t.Parallel()
		src := strings.Replace(algLegacyOverlayReceiver,
			"if !decodeProgressState(in) {",
			"annotate(in)\n\tif !decodeProgressState(in) {", 1) +
			"\nfunc annotate(in terminaldecision.Input) { _ = NewCompletionToolProvider() }\n"
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile:    src + "\n" + algPreferredOverlayConstructor,
			algPreferredProviderFile: algPreferredOverlayReceiver,
			algFeatureRootDir + "/completiontool_handle.go": `package agentloopguard

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controltool"

func NewCompletionToolProvider() controltool.Provider { return nil }
`,
		})
		findings := ScanAgentLoopGuardStrategyReachability(files, spec)
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGLegacyStrategyIsolation,
			`reaches "NewCompletionToolProvider"`),
			"legacy isolation ratchet must follow a hop into the control-tool entry point:\n%s", algFixtureRender(findings))
	})
}

// algPreferredOverlayConstructor is the shared miniature strategy dispatcher used
// by the legacy-isolation fixtures.
const algPreferredOverlayConstructor = `
func NewConfiguredProvider(cfg Config) (terminaldecision.Provider, error) {
	switch cfg.Strategy {
	case StrategySemanticVerifier:
		return provider{}, nil
	case StrategyAttemptCompletion:
		return preferredProvider{maxProtocolReprompts: 1}, nil
	default:
		return nil, nil
	}
}
`
