package planeparity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/planeparity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/stretchr/testify/require"
)

type harnessHook struct{ id string }

func (h harnessHook) ID() string                      { return h.id }
func (harnessHook) Order() int                        { return 0 }
func (harnessHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (harnessHook) Handle(context.Context, *lipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type harnessRequestPartHook struct{ id string }

func (h harnessRequestPartHook) ID() string                      { return h.id }
func (harnessRequestPartHook) Order() int                        { return 0 }
func (harnessRequestPartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (harnessRequestPartHook) HandleRequestParts(context.Context, *lipapi.Call, sdkhooks.PartMeta) error {
	return nil
}

type harnessResponsePartHook struct{ id string }

func (h harnessResponsePartHook) ID() string                      { return h.id }
func (harnessResponsePartHook) Order() int                        { return 0 }
func (harnessResponsePartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (harnessResponsePartHook) HandleEvent(context.Context, *lipapi.Event, sdkhooks.PartMeta) error {
	return nil
}

type harnessLifecycle struct{ id string }

func (harnessLifecycle) Start(context.Context) error { return nil }
func (harnessLifecycle) Stop(context.Context) error  { return nil }

type harnessTerminalProvider struct{ id string }

func (p harnessTerminalProvider) ID() string { return p.id }
func (harnessTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "done"}, nil
}

func TestAssertGeneratedSurfaceInvariants_Success(t *testing.T) {
	t.Parallel()

	cs1 := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs1, lipfeature.PlaneSubmitHooks, "b1", []sdkhooks.SubmitHook{harnessHook{id: "hook-1"}}))
	require.NoError(t, lipfeature.Contribute(cs1, lipfeature.PlaneRequestPartHooks, "b1", []sdkhooks.RequestPartHook{harnessRequestPartHook{id: "reqpart-1"}}))
	require.NoError(t, lipfeature.Contribute(cs1, lipfeature.PlaneResponsePartHooks, "b1", []sdkhooks.ResponsePartHook{harnessResponsePartHook{id: "resppart-1"}}))
	require.NoError(t, lipfeature.Contribute(cs1, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "b1", 4096))
	b1 := lipfeature.BundleFromPlanes(cs1.Freeze(), []lipplugin.Lifecycle{harnessLifecycle{id: "life-1"}})

	cs2 := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs2, lipfeature.PlaneSubmitHooks, "b2", []sdkhooks.SubmitHook{harnessHook{id: "hook-2"}}))
	require.NoError(t, lipfeature.Contribute(cs2, lipfeature.PlaneRequestPartHooks, "b2", []sdkhooks.RequestPartHook{harnessRequestPartHook{id: "reqpart-2"}}))
	require.NoError(t, lipfeature.Contribute(cs2, lipfeature.PlaneResponsePartHooks, "b2", []sdkhooks.ResponsePartHook{harnessResponsePartHook{id: "resppart-2"}}))
	require.NoError(t, lipfeature.Contribute(cs2, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "b2", 2048))
	require.NoError(t, lipfeature.Contribute(cs2, lipfeature.PlaneTerminalDecisionProvider, "b2", terminaldecision.Provider(harnessTerminalProvider{id: "term-1"})))
	b2 := lipfeature.BundleFromPlanes(cs2.Freeze(), []lipplugin.Lifecycle{harnessLifecycle{id: "life-2"}})

	planeparity.AssertGeneratedSurfaceInvariants(t, b1, b2)
}

func TestAssertGeneratedSurfaceInvariants_Conflict(t *testing.T) {
	t.Parallel()

	cs1 := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs1, lipfeature.PlaneTerminalDecisionProvider, "b1", terminaldecision.Provider(harnessTerminalProvider{id: "term-1"})))
	b1 := lipfeature.BundleFromPlanes(cs1.Freeze(), nil)

	cs2 := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs2, lipfeature.PlaneTerminalDecisionProvider, "b2", terminaldecision.Provider(harnessTerminalProvider{id: "term-2"})))
	b2 := lipfeature.BundleFromPlanes(cs2.Freeze(), nil)

	planeparity.AssertGeneratedSurfaceInvariants(t, b1, b2)
}

func TestMergeBundlesGenerated_Conflict(t *testing.T) {
	t.Parallel()

	cs1 := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs1, lipfeature.PlaneTerminalDecisionProvider, "b1", terminaldecision.Provider(harnessTerminalProvider{id: "term-1"})))
	b1 := lipfeature.BundleFromPlanes(cs1.Freeze(), nil)

	cs2 := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs2, lipfeature.PlaneTerminalDecisionProvider, "b2", terminaldecision.Provider(harnessTerminalProvider{id: "term-2"})))
	b2 := lipfeature.BundleFromPlanes(cs2.Freeze(), nil)

	res, err := featurebundle.MergeBundlesGenerated(b1, b2)
	require.Error(t, err)
	require.ErrorIs(t, err, lipfeature.ErrExclusiveConflict)
	require.Equal(t, featurebundle.GeneratedMergeSurface{}, res)
}

func TestAssertGeneratedSurfaceInvariants_MixedConflictPrecedesMalformedSchema(t *testing.T) {
	t.Parallel()

	// bundle0: valid terminal provider A
	cs0 := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs0, lipfeature.PlaneTerminalDecisionProvider, "bundle-0", terminaldecision.Provider(harnessTerminalProvider{id: "term-A"})))
	b0 := lipfeature.BundleFromPlanes(cs0.Freeze(), []lipplugin.Lifecycle{harnessLifecycle{id: "life-0"}})

	// bundle1: terminal conflict B (valid internally, but conflicts with b0)
	cs1 := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs1, lipfeature.PlaneTerminalDecisionProvider, "bundle-1", terminaldecision.Provider(harnessTerminalProvider{id: "term-B"})))
	b1 := lipfeature.BundleFromPlanes(cs1.Freeze(), []lipplugin.Lifecycle{harnessLifecycle{id: "life-1"}})

	// bundle2: malformed schema (invalid SchemaVersion 999 with non-empty planes)
	cs2 := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs2, lipfeature.PlaneSubmitHooks, "bundle-2", []sdkhooks.SubmitHook{harnessHook{id: "hook-2"}}))
	b2 := lipfeature.FeatureBundle{
		SchemaVersion: 999,
		PlaneSet:      cs2.Freeze(),
		Lifecycles:    []lipplugin.Lifecycle{harnessLifecycle{id: "life-2"}},
	}

	// Invariants assertion should succeed because conflict at bundle1 is evaluated and precedes malformed bundle2
	planeparity.AssertGeneratedSurfaceInvariants(t, b0, b1, b2)

	// Explicitly verify production behavior and error precedence
	res, err := featurebundle.MergeBundlesGenerated(b0, b1, b2)
	require.Error(t, err)
	require.ErrorIs(t, err, lipfeature.ErrExclusiveConflict)
	require.ErrorIs(t, err, lipfeature.ErrTerminalDecisionProviderConflict)
	require.Equal(t, featurebundle.GeneratedMergeSurface{}, res)
	require.Nil(t, res.Lifecycles)

	// Production derives incoming bundle terminal frozen identity before contribute, so pluginID is "term-B"
	var attrErr *lipfeature.AttributedError
	require.ErrorAs(t, err, &attrErr)
	require.Equal(t, "term-B", attrErr.PluginID)
	require.Equal(t, lipfeature.PlaneTerminalDecisionProvider.ID, attrErr.PlaneID)
	require.Contains(t, err.Error(), "term-A")
	require.Contains(t, err.Error(), "term-B")
}

func TestAssertGeneratedSurfaceInvariants_ReplayMutationAttackProvesOracleUnchanged(t *testing.T) {
	t.Parallel()

	// Initial valid bundle with multiple planes and lifecycles
	cs1 := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs1, lipfeature.PlaneSubmitHooks, "b1", []sdkhooks.SubmitHook{harnessHook{id: "hook-1"}}))
	require.NoError(t, lipfeature.Contribute(cs1, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "b1", 4096))
	require.NoError(t, lipfeature.Contribute(cs1, lipfeature.PlaneTerminalDecisionProvider, "b1", terminaldecision.Provider(harnessTerminalProvider{id: "term-1"})))
	b1 := lipfeature.BundleFromPlanes(cs1.Freeze(), []lipplugin.Lifecycle{harnessLifecycle{id: "life-1"}})

	// Attack bundle that contains a valid SubmitHook but also an exclusive conflict on TerminalDecisionProvider
	cs2 := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs2, lipfeature.PlaneSubmitHooks, "attacker", []sdkhooks.SubmitHook{harnessHook{id: "hook-attacker"}}))
	require.NoError(t, lipfeature.Contribute(cs2, lipfeature.PlaneTerminalDecisionProvider, "attacker", terminaldecision.Provider(harnessTerminalProvider{id: "term-2"})))
	b2 := lipfeature.BundleFromPlanes(cs2.Freeze(), []lipplugin.Lifecycle{harnessLifecycle{id: "life-attacker"}})

	// AssertGeneratedSurfaceInvariants asserts fail-before-mutate on oracleCS during replay error
	planeparity.AssertGeneratedSurfaceInvariants(t, b1, b2)

	// Verify fail-before-mutate directly on ContributionSet
	workingCS := lipfeature.NewContributionSet()
	require.NoError(t, b1.PlaneSet.ReplayTo(workingCS, "b1"))

	beforeFrozen := workingCS.Freeze()

	// Attempt replaying b2 should fail and leave workingCS unchanged
	err := b2.PlaneSet.ReplayTo(workingCS, "attacker")
	require.Error(t, err)
	require.ErrorIs(t, err, lipfeature.ErrExclusiveConflict)

	afterFrozen := workingCS.Freeze()
	require.Equal(t, lipfeature.Get(beforeFrozen, lipfeature.PlaneSubmitHooks), lipfeature.Get(afterFrozen, lipfeature.PlaneSubmitHooks))
	require.Equal(t, lipfeature.Get(beforeFrozen, lipfeature.PlaneTerminalDecisionProvider), lipfeature.Get(afterFrozen, lipfeature.PlaneTerminalDecisionProvider))
	require.Equal(t, lipfeature.Get(beforeFrozen, lipfeature.PlaneToolCallFinalizationMaxArgsBytes), lipfeature.Get(afterFrozen, lipfeature.PlaneToolCallFinalizationMaxArgsBytes))
	require.Equal(t, lipfeature.ProjectDiagnostics(beforeFrozen), lipfeature.ProjectDiagnostics(afterFrozen))
}

func TestAssertGeneratedSurfaceInvariants_ValidationFailure(t *testing.T) {
	t.Parallel()

	cs := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, "b0", []sdkhooks.SubmitHook{harnessHook{id: "hook-0"}}))
	b0 := lipfeature.FeatureBundle{
		SchemaVersion: 999,
		PlaneSet:      cs.Freeze(),
	}

	planeparity.AssertGeneratedSurfaceInvariants(t, b0)
}

func TestFreezeTestBundleChecked_InvalidTerminalProviderPanicsAndFails(t *testing.T) {
	t.Parallel()

	var nilTypedProvider *harnessTerminalProvider
	var prov terminaldecision.Provider = nilTypedProvider

	b := testkit.TestFeatureBundle{
		TerminalDecisionProvider: prov,
	}

	frozen, err := testkit.FreezeTestBundleChecked(b)
	require.Error(t, err)
	require.True(t, frozen.IsZero())

	require.Panics(t, func() {
		testkit.FreezeTestBundle(b)
	})
}

func TestFreezeTestBundleChecked_NegativeFinalizerCapReturnsError(t *testing.T) {
	t.Parallel()

	b := testkit.TestFeatureBundle{
		ToolCallFinalizationMaxArgsBytes: -10,
	}

	frozen, err := testkit.FreezeTestBundleChecked(b)
	require.Error(t, err)
	require.True(t, frozen.IsZero())

	require.Panics(t, func() {
		testkit.FreezeTestBundle(b)
	})
}

func TestFreezeTestBundleChecked_ExplicitNonNilEmptySlicePreserved(t *testing.T) {
	t.Parallel()

	b := testkit.TestFeatureBundle{
		SessionOpeners: []session.Opener{},
	}

	frozen, err := testkit.FreezeTestBundleChecked(b)
	require.NoError(t, err)
	require.False(t, frozen.IsZero())

	got := lipfeature.Get(frozen, lipfeature.PlaneSessionOpeners)
	require.NotNil(t, got)
	require.Len(t, got, 0)
}

func TestFreezeTestBundleChecked_NilSliceAbsent(t *testing.T) {
	t.Parallel()

	b := testkit.TestFeatureBundle{
		SessionOpeners: nil,
	}

	frozen, err := testkit.FreezeTestBundleChecked(b)
	require.NoError(t, err)

	got := lipfeature.Get(frozen, lipfeature.PlaneSessionOpeners)
	require.Nil(t, got)
}

func TestFreezeTestBundleChecked_CustomContributorIDInAttributedError(t *testing.T) {
	t.Parallel()

	var nilTypedProvider *harnessTerminalProvider
	var prov terminaldecision.Provider = nilTypedProvider

	b := testkit.TestFeatureBundle{
		ContributorID:            "custom-plugin-id",
		TerminalDecisionProvider: prov,
	}

	_, err := testkit.FreezeTestBundleChecked(b)
	require.Error(t, err)

	var attrErr *lipfeature.AttributedError
	require.ErrorAs(t, err, &attrErr)
	require.Equal(t, "custom-plugin-id", attrErr.PluginID)
	require.Equal(t, lipfeature.PlaneTerminalDecisionProvider.ID, attrErr.PlaneID)
}

func TestFreezeTestBundleChecked_EarlyValidThenInvalidReturnsZero(t *testing.T) {
	t.Parallel()

	var nilTypedProvider *harnessTerminalProvider
	var prov terminaldecision.Provider = nilTypedProvider

	b := testkit.TestFeatureBundle{
		ToolCallFinalizationMaxArgsBytes: 1024,
		TerminalDecisionProvider:         prov,
	}

	frozen, err := testkit.FreezeTestBundleChecked(b)
	require.Error(t, err)
	require.True(t, frozen.IsZero(), "returned frozen value must be zero on error")
}

func TestFeatureBundleHelper_ContractAndLifecyclePreservation(t *testing.T) {
	t.Parallel()

	// 1. Lifecycle nil vs empty preservation
	bNil := testkit.FeatureBundle(t, "my-feat", func(cs *lipfeature.ContributionSet) error {
		return lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, "my-feat", []sdkhooks.SubmitHook{harnessHook{id: "h1"}})
	}, nil)
	require.Nil(t, bNil.Lifecycles)

	bEmpty := testkit.FeatureBundle(t, "my-feat", func(cs *lipfeature.ContributionSet) error {
		return lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, "my-feat", []sdkhooks.SubmitHook{harnessHook{id: "h1"}})
	}, []lipplugin.Lifecycle{})
	require.NotNil(t, bEmpty.Lifecycles)
	require.Len(t, bEmpty.Lifecycles, 0)

	// 2. Callback error produces attributed error context with contributorID
	expectedErr := errors.New("boom")
	require.PanicsWithError(t, "testkit.FeatureBundle (failing-contrib): contribute: boom", func() {
		testkit.FeatureBundle(nil, "failing-contrib", func(cs *lipfeature.ContributionSet) error {
			return expectedErr
		}, nil)
	})
}
