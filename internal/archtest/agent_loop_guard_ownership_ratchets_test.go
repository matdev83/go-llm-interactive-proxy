package archtest

import (
	"strings"
	"testing"

	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live-tree ratchets for the agent-loop-explicit-completion-protocol ownership
// and strategy-isolation commitments (requirements 3.1-3.6, 9.4-9.6, 10.5,
// 12.1-12.4).
//
// Every rule below is proven to FIRE on a deliberately violating sample in the
// committed fixture files (agent_loop_guard_ownership_fixture_test.go,
// agent_loop_guard_feature_ownership_fixture_test.go and
// agent_loop_guard_strategy_isolation_fixture_test.go). A scan of the current
// tree alone only proves the property holds today, so the committed fixtures are
// the load-bearing half of each ratchet; the tree scan keeps the current state
// honest.
//
// Design ratchet -> owning check:
//
//	1  no ALG import/name/switch in generic core ....... TestTask121GenericCoreHasNoALGOwnership
//	2  controltool holds no concrete feature identity .. TestTask121ControlToolSDKHasNoALGDependency
//	3a control-tool plane admits at most one occupant ... TestTask121ControlToolPlaneIsExclusive
//	3b control-tool plane is never request-executed ..... TestTask121OwnershipRatchetsHoldOnCurrentTree
//	4  no direct ALG append to client/A-leg call ...... TestTask121OwnershipRatchetsHoldOnCurrentTree
//	5  no second terminal owner ....................... TestTask121OwnershipRatchetsHoldOnCurrentTree
//	6  no retired hidden-content owner or 2nd store .... TestTask121OwnershipRatchetsHoldOnCurrentTree
//	7  no verifier reachable from preferred strategy ... TestTask121OwnershipRatchetsHoldOnCurrentTree
//	8  no control-tool provider reachable from legacy .. TestTask121OwnershipRatchetsHoldOnCurrentTree
//
// Ratchets 7 and 8 are enforced over CALLS plus the receiver's own file imports,
// over a walk that follows plain same-package function hops and seeds the
// receiver's methods wholesale. It deliberately does not follow a method reached
// through a receiver variable, a method value, or any indirection the walk
// cannot attribute without types. That approximation, and why failing closed on
// it would misreport the approved live layout, is documented on
// ScanAgentLoopGuardStrategyReachability and pinned by a fixture.

// algOwnershipRatchetRuleRules binds each rule name to the design ratchet it
// enforces, so a finding can always be traced back to an approved commitment and
// a fixture can assert that its own defect is the one that fired.
var algOwnershipRatchetRuleRules = map[string]string{
	RuleALGCoreOwnership:              "design ratchet 1: no agentloopguard import/name/switch in internal/core generic production code",
	RuleALGControlToolSDKOwnership:    "design ratchet 2: the new controltool package contains no concrete feature IDs or names",
	RuleALGExclusiveControlToolPlane:  "design ratchet 3: the control-tool plane is an exclusive, identity-bearing, nil-rejecting slot",
	RuleALGControlToolPlaneExecution:  "requirement 3.5: the control-tool plane cannot be surfaced to the frontend execution path",
	RuleALGClientCallAppend:           "design ratchet 4: no direct ALG append to client/A-leg Call.Messages/Call.Items",
	RuleALGSecondTerminalOwner:        "requirement 9.5: no second terminal owner or separate terminal publication path",
	RuleALGHiddenGuardResurrection:    "design ratchet 5: no use of deprecated turnTerminal.guardHidden and no resurrection of it",
	RuleALGPolicyStoreOwnership:       "design ratchet 6: no second policy endpoint/store owned by the feature",
	RuleALGPreferredStrategyIsolation: "design ratchet 7: no verifier import in the preferred receiver's own files and no verifier CALL in its strategy case body, its methods, or the plain-function chain they reach",
	RuleALGLegacyStrategyIsolation:    "design ratchet 8: no control-tool import in the legacy receiver's own files and no control-tool CALL (or feature-local entry point) in its strategy case body, its methods, or the plain-function chain they reach",
}

// TestTask121OwnershipRatchetsHoldOnCurrentTree runs every ALG ownership
// ratchet against the live production tree. The committed fixtures prove each
// rule fires; this test proves the current tree satisfies all of them.
func TestTask121OwnershipRatchetsHoldOnCurrentTree(t *testing.T) {
	t.Parallel()
	findings, err := ScanAgentLoopGuardOwnershipViolations(repoRoot(t))
	require.NoError(t, err)
	if len(findings) > 0 {
		var rendered []string
		for _, finding := range findings {
			rendered = append(rendered, finding.String())
		}
		t.Fatalf("ALG ownership ratchets violated on the current tree (%d):\n%s", len(findings), strings.Join(rendered, "\n"))
	}
}

// TestTask121OwnershipRatchetRulesAreAllBound guards the rule registry itself:
// an unbound rule name would let a finding be reported with no traceable
// commitment, and a bound-but-unimplemented rule would let a fixture pass
// vacuously.
func TestTask121OwnershipRatchetRulesAreAllBound(t *testing.T) {
	t.Parallel()
	assert.Len(t, algOwnershipRatchetRuleRules, 10)
	for _, rule := range []string{
		RuleALGCoreOwnership,
		RuleALGControlToolSDKOwnership,
		RuleALGExclusiveControlToolPlane,
		RuleALGControlToolPlaneExecution,
		RuleALGClientCallAppend,
		RuleALGSecondTerminalOwner,
		RuleALGHiddenGuardResurrection,
		RuleALGPolicyStoreOwnership,
		RuleALGPreferredStrategyIsolation,
		RuleALGLegacyStrategyIsolation,
	} {
		assert.Contains(t, algOwnershipRatchetRuleRules, rule)
	}
}

// TestTask121GenericCoreHasNoALGOwnership proves design ratchet 1 directly, so
// the core boundary does not depend only on the aggregate tree scan. It covers
// all of internal/core, not just internal/core/runtime: requirement 12.1 forbids
// provider-name and feature-name switches in generic terminal, tool, or stream
// logic wherever that logic lives.
func TestTask121GenericCoreHasNoALGOwnership(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var offenders []string
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		if !MatchPathPrefix(PackageDirFromRel(rel), algCoreDir) {
			return nil
		}
		fset, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		for _, finding := range ScanFileAgentLoopGuardOwnership(rel, fset, f) {
			if finding.Rule == RuleALGCoreOwnership {
				offenders = append(offenders, finding.String())
			}
		}
		return nil
	})
	require.NoError(t, err)
	if len(offenders) > 0 {
		t.Fatalf("generic core names or imports the concrete ALG feature (%d):\n%s", len(offenders), strings.Join(offenders, "\n"))
	}
}

// TestTask121ControlToolSDKHasNoALGDependency proves design ratchet 2 directly:
// pkg/lipsdk/controltool carries no dependency at all on the ALG feature.
func TestTask121ControlToolSDKHasNoALGDependency(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var offenders []string
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		if PackageDirFromRel(rel) != algControlToolSDK {
			return nil
		}
		fset, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		for _, finding := range ScanFileAgentLoopGuardOwnership(rel, fset, f) {
			if finding.Rule == RuleALGControlToolSDKOwnership {
				offenders = append(offenders, finding.String())
			}
		}
		return nil
	})
	require.NoError(t, err)
	if len(offenders) > 0 {
		t.Fatalf("the generic control-tool SDK contract depends on ALG (%d):\n%s", len(offenders), strings.Join(offenders, "\n"))
	}
}

// TestTask121ControlToolPlaneIsExclusive proves the first half of design ratchet
// 3 declaratively: the control-tool plane is one exclusive, identity-bearing,
// nil-rejecting slot contributed only from the feature source, and it appears
// exactly once in the standard plane list. The second half - that it is never
// surfaced to the frontend execution path - is a per-file rule inside the tree
// scan above.
//
// The behavioral exclusivity proof (a second Contribute is refused) already
// exists in internal/standardplugins/agentloopguard_feature_red_test.go; this
// ratchet pins the declaration that makes it true.
func TestTask121ControlToolPlaneIsExclusive(t *testing.T) {
	t.Parallel()
	findings := ValidateAlgControlToolExclusivePlane(liveAlgControlToolPlaneShape())
	if len(findings) > 0 {
		var rendered []string
		for _, finding := range findings {
			rendered = append(rendered, finding.String())
		}
		t.Fatalf("control-tool plane declaration is not exclusive (%d):\n%s", len(findings), strings.Join(rendered, "\n"))
	}
}

// liveAlgControlToolPlaneShape projects the live plane declaration into the
// plain shape the exclusive-slot validator consumes.
func liveAlgControlToolPlaneShape() algControlToolPlaneShape {
	plane := lipfeature.PlaneControlToolProvider
	uses := 0
	for _, declared := range lipfeature.StandardPlanes {
		if declared.PlaneID() == plane.ID {
			uses++
		}
	}
	return algControlToolPlaneShape{
		ID:                plane.ID,
		Multiplicity:      plane.Multiplicity.String(),
		FeatureRule:       plane.Rules.RuleFor(lipfeature.SourceFeature).String(),
		NilPolicy:         plane.NilPolicy.String(),
		HasIdentity:       plane.Identity != nil,
		StandardPlaneUses: uses,
	}
}
