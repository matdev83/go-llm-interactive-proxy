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

func TestAssertDualPathParity_Success(t *testing.T) {
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

	planeparity.AssertDualPathParity(t, b1, b2)
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

func TestMergeBundlesViaGenerated_Equivalence(t *testing.T) {
	t.Parallel()

	cs1 := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs1, lipfeature.PlaneSubmitHooks, "b1", []sdkhooks.SubmitHook{harnessHook{id: "h1"}}))
	require.NoError(t, lipfeature.Contribute(cs1, lipfeature.PlaneRequestPartHooks, "b1", []sdkhooks.RequestPartHook{harnessRequestPartHook{id: "rp1"}}))
	require.NoError(t, lipfeature.Contribute(cs1, lipfeature.PlaneResponsePartHooks, "b1", []sdkhooks.ResponsePartHook{harnessResponsePartHook{id: "rsp1"}}))
	require.NoError(t, lipfeature.Contribute(cs1, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "b1", 8192))
	require.NoError(t, lipfeature.Contribute(cs1, lipfeature.PlaneTerminalDecisionProvider, "b1", terminaldecision.Provider(harnessTerminalProvider{id: "term-1"})))
	b1 := lipfeature.BundleFromPlanes(cs1.Freeze(), []lipplugin.Lifecycle{harnessLifecycle{id: "l1"}})

	cs2 := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs2, lipfeature.PlaneSubmitHooks, "b2", []sdkhooks.SubmitHook{harnessHook{id: "h2"}}))
	require.NoError(t, lipfeature.Contribute(cs2, lipfeature.PlaneRequestPartHooks, "b2", []sdkhooks.RequestPartHook{harnessRequestPartHook{id: "rp2"}}))
	require.NoError(t, lipfeature.Contribute(cs2, lipfeature.PlaneResponsePartHooks, "b2", []sdkhooks.ResponsePartHook{harnessResponsePartHook{id: "rsp2"}}))
	require.NoError(t, lipfeature.Contribute(cs2, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "b2", 4096))
	b2 := lipfeature.BundleFromPlanes(cs2.Freeze(), []lipplugin.Lifecycle{harnessLifecycle{id: "l2"}})

	legacy, errLegacy := featurebundle.MergeBundlesChecked(b1, b2)
	require.NoError(t, errLegacy)

	viaGen, errGen := featurebundle.MergeBundlesViaGenerated(b1, b2)
	require.NoError(t, errGen)

	require.Equal(t, legacy, viaGen)
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
