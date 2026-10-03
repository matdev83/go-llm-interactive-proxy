package archtest

// Whole-feature-tree enforcement fixtures for the ALG multi-file ratchets:
// the terminal-owner census (design ratchet 5 / Requirement 9.5) and both
// strategy-isolation walks (design ratchets 7 and 8).
//
// These three ratchets are only as wide as the file set the repository walk
// hands them. A validator fed a hand-built overlay always sees every file the
// fixture chose to give it, so a validator-level fixture cannot observe a walk
// that drops the feature's subpackages. The gap that matters is therefore in
// the WALK: if the walk's index holds only the root package, a Decide receiver
// or a verifier call placed in agentloopguard/protocolpolicy/ is invisible to
// all three ratchets at once.
//
// So every test below runs ScanAgentLoopGuardOwnershipViolations - the exact
// entry point the committed architecture gate runs - over a miniature
// repository written under t.TempDir, and asserts the verdict on both a
// violating tree and its otherwise-identical compliant sibling. That is the
// only shape in which "the walk can see a subpackage" is a claim about the
// shipped ratchet rather than about the fixture's own file list.
//
// The compliant sibling matters as much as the violation: a widened index is
// only correct if it still accepts the shipped layout, which is what keeps the
// fix from becoming a blanket refusal.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// algFeatureTreeFixtureRoot materialises a miniature repository whose only
// content is the supplied repo-relative production sources, and returns its
// root. The walk reads ProductionScanRoots (cmd, internal, pkg) from disk, so a
// subpackage is only reachable at its real repo-relative path.
func algFeatureTreeFixtureRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range sortedKeys(files) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750), "create fixture dir for %s", rel)
		require.NoError(t, os.WriteFile(path, []byte(files[rel]), 0o600), "write fixture %s", rel)
	}
	return root
}

// algFeatureTreeScan runs the shipped repository scan over the fixture tree.
func algFeatureTreeScan(t *testing.T, files map[string]string) []RuleFinding {
	t.Helper()
	findings, err := ScanAgentLoopGuardOwnershipViolations(algFeatureTreeFixtureRoot(t, files))
	require.NoError(t, err)
	return findings
}

// algFixtureHasRuleUnderPath reports whether any finding of rule names a file
// under the given repo-relative path. It is the file-attribution half of the
// verdict: a ratchet that fires without naming the offending file has not proved
// it can see that file at all.
func algFixtureHasRuleUnderPath(findings []RuleFinding, rule, path string) bool {
	for _, finding := range findings {
		if finding.Rule == rule && strings.HasPrefix(finding.Path, path) {
			return true
		}
	}
	return false
}

// algTreeProtocolPolicyDir is the subpackage used throughout: it is the
// preferred strategy's policy owner in the shipped layout, so a violation
// planted here is exactly the shape the root-only index could not see.
const algTreeProtocolPolicyDir = algFeatureRootDir + "/protocolpolicy"

// algTreeProtocolPolicyClean is the compliant protocol policy: a pure value
// policy with no verifier, no control tool, and no terminal decision of its own.
const algTreeProtocolPolicyClean = `package protocolpolicy

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"

// Result is the bounded policy verdict.
type Result struct{ Reason string }

// Evaluate classifies one canonical terminal candidate.
func Evaluate(in terminaldecision.Input) Result {
	if in.Continuation.Attempt == 0 {
		return Result{Reason: "protocol_inactive"}
	}
	return Result{}
}
`

// algTreeProtocolPolicyVerifierReach plants the highest-risk shape the
// validation report named: the verifier is called from inside the pure policy
// SUBPACKAGE, not from the preferred receiver's own file, so a walk that stops
// at a cross-package selector reports nothing.
const algTreeProtocolPolicyVerifierReach = `package protocolpolicy

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard/verifier"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

// Result is the bounded policy verdict.
type Result struct{ Reason string }

// Evaluate reaches the legacy verifier from the pure policy subpackage.
func Evaluate(ctx context.Context, in terminaldecision.Input) Result {
	if _, err := verifier.New(in.Auxiliary, verifier.Config{}).Verify(ctx, in); err != nil {
		return Result{Reason: "verifier_error"}
	}
	return Result{}
}
`

// algTreeProtocolPolicyTerminalOwner plants a second terminal owner in the
// subpackage: a receiver that declares the generic terminal-decision method and
// therefore satisfies the same terminaldecision.Provider seam.
const algTreeProtocolPolicyTerminalOwner = `package protocolpolicy

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

type policyOwner struct{}

func (policyOwner) Decide(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{}, nil
}
`

// algTreeProgressControlToolReach plants the mirror-image violation for the
// legacy strategy: the proxy-owned control tool is reached from a subpackage
// function the legacy receiver calls.
const algTreeProgressControlToolReach = `package progress

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controltool"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

// Evaluate reaches the proxy-owned control tool from a subpackage.
func Evaluate(in terminaldecision.Input) (terminaldecision.Decision, error) {
	if err := controltool.ValidateOutcome(terminaldecision.Decision{}); err != nil {
		return terminaldecision.Decision{}, err
	}
	return terminaldecision.Decision{}, nil
}
`

// algTreeProviderIntoPolicy is the miniature approved root package whose
// PREFERRED receiver delegates the decision to the policy subpackage, which is
// the shipped layout.
func algTreeProviderIntoPolicy(t *testing.T) string {
	t.Helper()
	src := strings.Replace(algPreferredOverlayReceiver,
		"	limit := p.limits()",
		"	verdict := protocolpolicy.Evaluate(in)\n"+
			"	if verdict.Reason != \"\" {\n"+
			"		return allowStop(verdict.Reason), nil\n"+
			"	}\n"+
			"	limit := p.limits()", 1)
	src = strings.Replace(src,
		`"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"`,
		`"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard/protocolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"`, 1)
	require.Contains(t, src, "protocolpolicy.Evaluate(in)", "the fixture must actually delegate to the subpackage")
	return src
}

// algTreeLegacyProviderIntoProgress is the miniature legacy receiver delegating
// to the progress subpackage.
func algTreeLegacyProviderIntoProgress(t *testing.T) string {
	t.Helper()
	src := strings.Replace(algPreferredOverlayProvider,
		`	return allowStop("legacy"), nil`,
		"	if _, err := progress.Evaluate(in); err != nil {\n"+
			"		return allowStop(\"legacy\"), err\n"+
			"	}\n"+
			"	return allowStop(\"legacy\"), nil", 1)
	src = strings.Replace(src,
		`import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"`,
		`import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard/progress"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)`, 1)
	require.Contains(t, src, "progress.Evaluate(in)", "the fixture must actually delegate to the subpackage")
	return src
}

// TestTask12FixtureALGScansWholeFeatureTree proves the repository scan's
// feature-file index covers the WHOLE feature, not only the root package, so
// design ratchets 5, 7 and 8 hold in the subpackages the shipped layout
// actually uses.
func TestTask12FixtureALGScansWholeFeatureTree(t *testing.T) {
	t.Parallel()

	t.Run("compliant root and subpackage layout passes", func(t *testing.T) {
		t.Parallel()
		findings := algFeatureTreeScan(t, map[string]string{
			algLegacyProviderFile:                           algPreferredOverlayProvider,
			algPreferredProviderFile:                        algTreeProviderIntoPolicy(t),
			algTreeProtocolPolicyDir + "/protocolpolicy.go": algTreeProtocolPolicyClean,
		})
		assert.Empty(t, algFixtureRender(findings))
	})

	t.Run("a Decide receiver in a subpackage is a second terminal owner", func(t *testing.T) {
		t.Parallel()
		findings := algFeatureTreeScan(t, map[string]string{
			algLegacyProviderFile:                           algPreferredOverlayProvider,
			algPreferredProviderFile:                        algPreferredOverlayReceiver,
			algTreeProtocolPolicyDir + "/protocolpolicy.go": algTreeProtocolPolicyTerminalOwner,
		})
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGSecondTerminalOwner,
			`unapproved terminal-policy receiver "policyOwner"`),
			"a subpackage receiver on the terminal-decision seam is a second terminal owner:\n%s", algFixtureRender(findings))
		assert.True(t, algFixtureHasRuleUnderPath(findings, RuleALGSecondTerminalOwner,
			algTreeProtocolPolicyDir+"/protocolpolicy.go"),
			"the finding must name the subpackage file that declares it:\n%s", algFixtureRender(findings))
	})

	t.Run("a verifier call in a subpackage reached from preferred is refused", func(t *testing.T) {
		t.Parallel()
		findings := algFeatureTreeScan(t, map[string]string{
			algLegacyProviderFile:                           algPreferredOverlayProvider,
			algPreferredProviderFile:                        algTreeProviderIntoPolicy(t),
			algTreeProtocolPolicyDir + "/protocolpolicy.go": algTreeProtocolPolicyVerifierReach,
		})
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGPreferredStrategyIsolation,
			`calls verifier.New from the forbidden dependency`),
			"the preferred isolation walk must follow a cross-package hop into the subpackage:\n%s", algFixtureRender(findings))
		assert.True(t, algFixtureHasRuleUnderPath(findings, RuleALGPreferredStrategyIsolation,
			algTreeProtocolPolicyDir+"/protocolpolicy.go"),
			"the finding must name the subpackage function that reached the verifier:\n%s", algFixtureRender(findings))
	})

	t.Run("a control-tool call in a subpackage reached from legacy is refused", func(t *testing.T) {
		t.Parallel()
		findings := algFeatureTreeScan(t, map[string]string{
			algLegacyProviderFile:                       algTreeLegacyProviderIntoProgress(t),
			algPreferredProviderFile:                    algPreferredOverlayReceiver,
			algFeatureRootDir + "/progress/progress.go": algTreeProgressControlToolReach,
		})
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGLegacyStrategyIsolation,
			`calls controltool.ValidateOutcome from the forbidden dependency`),
			"the legacy isolation walk must follow a cross-package hop into the subpackage:\n%s", algFixtureRender(findings))
	})

	t.Run("a verifier call in an unreached subpackage stays unreported", func(t *testing.T) {
		t.Parallel()
		// The legacy strategy owns the verifier. The same subpackage file is
		// therefore legal for the feature as a whole; only the PREFERRED walk
		// refuses it, and only when it is actually reachable from the preferred
		// receiver. This is what keeps the widened walk a reachability ratchet
		// instead of a feature-wide import ban.
		findings := algFeatureTreeScan(t, map[string]string{
			algLegacyProviderFile:    algPreferredOverlayProvider,
			algPreferredProviderFile: algPreferredOverlayReceiver,
			algFeatureRootDir + "/verifier/verifier.go": `package verifier

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

type Adapter struct{}

func New(aux auxiliary.Requester, cfg Config) *Adapter { return &Adapter{} }

func (a *Adapter) Verify(ctx context.Context, in terminaldecision.Input) (Verdict, error) {
	return Verdict{}, nil
}

type Config struct{}

type Verdict struct{}
`,
		})
		assert.Empty(t, algFixtureRender(findings))
	})

	t.Run("a same-named declaration in another subpackage cannot shadow the reached one", func(t *testing.T) {
		t.Parallel()
		// The index is keyed by package plus name precisely because the feature
		// is decomposed: two packages may each declare Evaluate. A name-only key
		// would keep one and drop the other, and which one survives would depend
		// on walk order - so a subpackage declaration could be invisible exactly
		// as it was before the index was widened. A clean decoy in verifier/
		// alongside the violating declaration in protocolpolicy/ proves the
		// reached one is the one judged.
		findings := algFeatureTreeScan(t, map[string]string{
			algLegacyProviderFile:                           algPreferredOverlayProvider,
			algPreferredProviderFile:                        algTreeProviderIntoPolicy(t),
			algTreeProtocolPolicyDir + "/protocolpolicy.go": algTreeProtocolPolicyVerifierReach,
			algFeatureRootDir + "/verifier/verifier.go": `package verifier

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"

type Result struct{ Reason string }

func Evaluate(in terminaldecision.Input) Result { return Result{} }
`,
		})
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasRuleUnderPath(findings, RuleALGPreferredStrategyIsolation,
			algTreeProtocolPolicyDir+"/protocolpolicy.go"),
			"the reached declaration must be judged in its own package:\n%s", algFixtureRender(findings))
	})
}
