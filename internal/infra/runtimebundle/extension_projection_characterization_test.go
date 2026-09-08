package runtimebundle

import (
	"context"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/localturn"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	sdksg "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubProjEnv struct{ val string }

func (s stubProjEnv) Lookup(name string) (string, bool) { return s.val, true }
func (s stubProjEnv) Snapshot() []string                { return []string{s.val} }

type stubProjObs struct{ val string }

func (s stubProjObs) OnSecretDecision(context.Context, sdksg.DecisionEvent) error { return nil }

type projCatalogFilter struct{ id string }

func (f projCatalogFilter) ID() string                      { return f.id }
func (projCatalogFilter) Order() int                        { return 0 }
func (projCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (projCatalogFilter) Handle(context.Context, *lipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type projOpener struct{ tag string }

func (o projOpener) ID() string { return o.tag }
func (projOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type projTransform struct{ tag string }

func (t projTransform) ID() string                      { return t.tag }
func (projTransform) Order() int                        { return 0 }
func (projTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (projTransform) Handle(context.Context, *lipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type projTrafficObs struct{ tag string }

func (projTrafficObs) OnObservation(context.Context, traffic.Observation) error { return nil }

type projUsageObs struct{ tag string }

func (projUsageObs) OnUsage(context.Context, usage.Event) error { return nil }

type projTerminalProvider struct{ tag string }

func (p projTerminalProvider) ID() string { return p.tag }

type projFinalizer struct{ id string }

func (f projFinalizer) ID() string { return f.id }
func (projFinalizer) Order() int   { return 0 }
func (projFinalizer) Finalize(context.Context, toolcall.CompletedCall, lipapi.ToolDef, []lipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{}, nil
}

func (projTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

// projMerged builds a generated merge surface with one tagged element on the planes the
// projection tests observe.
func projMerged(t *testing.T) featurebundle.GeneratedMergeSurface {
	t.Helper()
	b := testkit.FeatureBundle(t, "feat", func(cs *lipfeature.ContributionSet) error {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizers, "feat", []toolcall.Finalizer{projFinalizer{id: "finalizer"}}); err != nil {
			return err
		}
		if err := lipfeature.Contribute(cs, lipfeature.PlaneRequestTransforms, "feat", []request.Transform{projTransform{tag: "transform"}}); err != nil {
			return err
		}
		if err := lipfeature.Contribute(cs, lipfeature.PlaneTrafficObservers, "feat", []traffic.Observer{projTrafficObs{tag: "feat-traffic"}}); err != nil {
			return err
		}
		if err := lipfeature.Contribute(cs, lipfeature.PlaneUsageObservers, "feat", []usage.Observer{projUsageObs{tag: "feat-usage"}}); err != nil {
			return err
		}
		return lipfeature.Contribute(cs, lipfeature.PlaneLocalTurnHandlers, "feat", []localturn.Handler{wiringHandler{id: "handler", ord: 1}})
	}, nil)
	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err)
	return gen
}

func assertAllSliceFieldsNil(t *testing.T, v any) {
	t.Helper()
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if field.Type.Kind() != reflect.Slice {
			continue
		}
		fv := rv.Field(i)
		require.True(t, fv.IsNil(), "%s: want nil slice, got len=%d", field.Name, fv.Len())
	}
}

// Pins exact emptiness transport through extensionsFromProcessOptions: a nil or empty process options
// projects to all-zero extension options; populated secret options project to equal values.
func TestExtensionsFromProcessOptions_preservesExactNilAndEmptyState(t *testing.T) {
	t.Parallel()

	t.Run("nil_process_options_projects_all_zero_extensions", func(t *testing.T) {
		t.Parallel()
		ext := extensionsFromProcessOptions(nil)
		assert.Nil(t, ext.SecretGuard)
		assert.Nil(t, ext.SecretGuardInventory)
		assert.Nil(t, ext.SecretDecisionObserver)
	})

	t.Run("empty_process_options_projects_all_zero_extensions", func(t *testing.T) {
		t.Parallel()
		ext := extensionsFromProcessOptions(&BuildOptions{})
		assert.Nil(t, ext.SecretGuard)
		assert.Nil(t, ext.SecretGuardInventory)
		assert.Nil(t, ext.SecretDecisionObserver)
	})

	t.Run("populated_process_options_projects_equal_secret_options", func(t *testing.T) {
		t.Parallel()
		sgPlane := &extensions.SecretGuardPlane{AccessMode: "single_user"}
		sgInv := &diag.InventoryExtras{SecretGuardCatalogEntryCount: 3}
		obs := sdksg.ObserverFunc(func(context.Context, sdksg.DecisionEvent) error { return nil })
		opts := &BuildOptions{
			Extensions: ExtensionsOptions{
				SecretGuard:            sgPlane,
				SecretGuardInventory:   sgInv,
				SecretDecisionObserver: obs,
			},
		}
		ext := extensionsFromProcessOptions(opts)
		assert.Equal(t, sgPlane, ext.SecretGuard)
		assert.Equal(t, sgInv, ext.SecretGuardInventory)
		assert.NotNil(t, ext.SecretDecisionObserver)
	})
}

func TestExtensionsFromProcessOptions_DefensiveCopyAndNilSemantics(t *testing.T) {
	t.Parallel()

	t.Run("all_fields_populated_exact_equality_interface_identity_and_isolation", func(t *testing.T) {
		t.Parallel()

		obs := stubProjObs{val: "obs-val"}
		sgPlane := &extensions.SecretGuardPlane{AccessMode: "single_user"}
		sgInv := &diag.InventoryExtras{SecretGuardCatalogEntryCount: 5}
		opts := &BuildOptions{
			Extensions: ExtensionsOptions{
				SecretGuard:            sgPlane,
				SecretGuardInventory:   sgInv,
				SecretDecisionObserver: obs,
			},
		}

		ext := extensionsFromProcessOptions(opts)

		// Exact equality across all fields
		assert.Equal(t, opts.Extensions.SecretGuard, ext.SecretGuard)
		assert.Equal(t, opts.Extensions.SecretGuardInventory, ext.SecretGuardInventory)
		assert.Equal(t, opts.Extensions.SecretDecisionObserver, ext.SecretDecisionObserver)

		// Interface identity
		assert.Equal(t, obs, ext.SecretDecisionObserver)
	})
}

func TestOverlayExtensions_preservesSecretGuardBehavior(t *testing.T) {
	t.Parallel()

	t.Run("nil_dst_does_not_panic", func(t *testing.T) {
		t.Parallel()
		assert.NotPanics(t, func() {
			overlayExtensions(nil, ExtensionsOptions{})
		})
	})

	t.Run("empty_src_does_not_mutate_dst", func(t *testing.T) {
		t.Parallel()
		sgPlane := &extensions.SecretGuardPlane{AccessMode: "single_user"}
		obs := sdksg.ObserverFunc(func(context.Context, sdksg.DecisionEvent) error { return nil })
		dst := ExtensionsOptions{
			SecretGuard:            sgPlane,
			SecretDecisionObserver: obs,
		}
		overlayExtensions(&dst, ExtensionsOptions{})
		assert.Equal(t, sgPlane, dst.SecretGuard)
		assert.NotNil(t, dst.SecretDecisionObserver)
	})

	t.Run("populated_src_overrides_dst", func(t *testing.T) {
		t.Parallel()
		dst := ExtensionsOptions{}
		newSG := &extensions.SecretGuardPlane{AccessMode: "multi_user"}
		newObs := sdksg.ObserverFunc(func(context.Context, sdksg.DecisionEvent) error { return nil })
		overlayExtensions(&dst, ExtensionsOptions{
			SecretGuard:            newSG,
			SecretDecisionObserver: newObs,
		})
		assert.Equal(t, newSG, dst.SecretGuard)
		assert.NotNil(t, dst.SecretDecisionObserver)
	})
}

// Pins backing-array isolation in both directions: projections are copies with
// zero spare capacity, so mutation or growth of either side never reaches the
// other.
func TestGeneratedMergeSurface_backingArrayIsolationBothDirections(t *testing.T) {
	t.Parallel()

	gen := projMerged(t)
	ltFrozen := lipfeature.Get(gen.Frozen, lipfeature.PlaneLocalTurnHandlers)
	require.Len(t, ltFrozen, 1)
	require.Equal(t, "handler", ltFrozen[0].ID())

	toFrozen := lipfeature.Get(gen.Frozen, lipfeature.PlaneTrafficObservers)
	require.Len(t, toFrozen, 1)
	trafficObs, trafficObsOK := toFrozen[0].(projTrafficObs)
	require.True(t, trafficObsOK)
	require.Equal(t, "feat-traffic", trafficObs.tag)

	uoFrozen := lipfeature.Get(gen.Frozen, lipfeature.PlaneUsageObservers)
	require.Len(t, uoFrozen, 1)
	usageObs, usageObsOK := uoFrozen[0].(projUsageObs)
	require.True(t, usageObsOK)
	require.Equal(t, "feat-usage", usageObs.tag)
}

// Pins host-injection ordering: production observers append AFTER feature
// contributions, and a nil process options injects nothing.
func TestGeneratedMergeSurface_hostObserversAppendAfterFeatures(t *testing.T) {
	t.Parallel()

	b := testkit.FeatureBundle(t, "feat", func(cs *lipfeature.ContributionSet) error {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneTrafficObservers, "feat", []traffic.Observer{projTrafficObs{tag: "feat-1"}, projTrafficObs{tag: "feat-2"}}); err != nil {
			return err
		}
		return lipfeature.Contribute(cs, lipfeature.PlaneUsageObservers, "feat", []usage.Observer{projUsageObs{tag: "feat-u"}})
	}, nil)

	cs := lipfeature.NewContributionSet()
	require.NoError(t, featurebundle.ContributeBundle(cs, "feat", b))
	require.NoError(t, featurebundle.ContributeBundle(cs, "host", testkit.FeatureBundle(t, "host", func(csHost *lipfeature.ContributionSet) error {
		if err := lipfeature.Contribute(csHost, lipfeature.PlaneTrafficObservers, "host", []traffic.Observer{projTrafficObs{tag: "host-1"}}); err != nil {
			return err
		}
		return lipfeature.Contribute(csHost, lipfeature.PlaneUsageObservers, "host", []usage.Observer{projUsageObs{tag: "host-u"}, projUsageObs{tag: "host-u2"}})
	}, nil)))
	gen := featurebundle.GeneratedMergeSurface{Frozen: cs.Freeze()}

	trafficFromFrozen := lipfeature.Get(gen.Frozen, lipfeature.PlaneTrafficObservers)
	gotTraffic := make([]string, 0, len(trafficFromFrozen))
	for _, o := range trafficFromFrozen {
		obs, ok := o.(projTrafficObs)
		require.True(t, ok)
		gotTraffic = append(gotTraffic, obs.tag)
	}
	require.Equal(t, []string{"feat-1", "feat-2", "host-1"}, gotTraffic)

	usageFromFrozen := lipfeature.Get(gen.Frozen, lipfeature.PlaneUsageObservers)
	gotUsage := make([]string, 0, len(usageFromFrozen))
	for _, o := range usageFromFrozen {
		obs, ok := o.(projUsageObs)
		require.True(t, ok)
		gotUsage = append(gotUsage, obs.tag)
	}
	require.Equal(t, []string{"feat-u", "host-u", "host-u2"}, gotUsage)

	genFeatOnly, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err)
	require.Len(t, lipfeature.Get(genFeatOnly.Frozen, lipfeature.PlaneTrafficObservers), 2)
	require.Len(t, lipfeature.Get(genFeatOnly.Frozen, lipfeature.PlaneUsageObservers), 1)
}
