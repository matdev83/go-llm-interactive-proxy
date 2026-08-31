package featurebundle

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

// TestFeatureBundle_SchemaNegotiation_NonEmptySchema0Rejected characterizes Requirement 2.4 (Task 1.2, Task 2.2):
// If a non-empty feature-plane bundle declares the compatibility zero version (SchemaVersion 0),
// the feature composition system shall reject it before publishing any contribution.
func TestFeatureBundle_SchemaNegotiation_NonEmptySchema0Rejected(t *testing.T) {
	t.Parallel()

	emptyFrozen := lipfeature.NewContributionSet().Freeze()

	t.Run("DirectContributeBundle", func(t *testing.T) {
		t.Parallel()
		planes := makeSchemaTestPlaneSet(t, "hook-bad-0")
		bundle := lipfeature.FeatureBundle{
			SchemaVersion: 0,
			PlaneSet:      planes,
		}

		cs := lipfeature.NewContributionSet()
		err := ContributeBundle(cs, "contrib-bad-0", bundle)
		require.Error(t, err, "non-empty feature-plane bundle with schema 0 must be rejected")
		require.Contains(t, err.Error(), "contrib-bad-0", "error must identify the responsible contributor")
		require.Equal(t, emptyFrozen, cs.Freeze(), "ContributionSet must remain empty on error")
	})

	t.Run("MergeBundlesGenerated_Single", func(t *testing.T) {
		t.Parallel()
		planes := makeSchemaTestPlaneSet(t, "hook-bad-0")
		bundle := lipfeature.FeatureBundle{
			SchemaVersion: 0,
			PlaneSet:      planes,
		}

		gen, err := MergeBundlesGenerated(bundle)
		require.Error(t, err, "MergeBundlesGenerated with non-empty schema 0 bundle must fail")
		require.Contains(t, err.Error(), "bundle-0", "error must identify contributor bundle-0")
		require.True(t, gen.Frozen.IsZero(), "returned GeneratedMergeSurface must be zero on failure")
		require.Empty(t, gen.Lifecycles, "no lifecycles should be published on failure")
	})

	t.Run("MergeBundlesGenerated_SecondBundleAttribution", func(t *testing.T) {
		t.Parallel()
		validBundle := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			PlaneSet:      makeSchemaTestPlaneSet(t, "valid-0"),
		}
		malformedBundle := lipfeature.FeatureBundle{
			SchemaVersion: 0,
			PlaneSet:      makeSchemaTestPlaneSet(t, "bad-1"),
		}

		gen, err := MergeBundlesGenerated(validBundle, malformedBundle)
		require.Error(t, err, "MergeBundlesGenerated with malformed second bundle must fail")
		require.Contains(t, err.Error(), "bundle-1", "error must identify contributor bundle-1")
		require.True(t, gen.Frozen.IsZero(), "returned GeneratedMergeSurface must be zero on failure")
		require.Empty(t, gen.Lifecycles, "no lifecycles should be published on failure")
	})
}

// TestFeatureBundle_SchemaNegotiation_UnsupportedSchemaRejected characterizes Requirement 2.6 (Task 1.2, Task 2.2):
// If any bundle declares an unsupported schema version (e.g. 2 or negative), the feature composition
// system shall reject it before publishing planes or lifecycles.
func TestFeatureBundle_SchemaNegotiation_UnsupportedSchemaRejected(t *testing.T) {
	t.Parallel()

	emptyFrozen := lipfeature.NewContributionSet().Freeze()

	t.Run("DirectContribute_NonEmpty_Schema2", func(t *testing.T) {
		t.Parallel()
		planes := makeSchemaTestPlaneSet(t, "hook-bad-2")
		bundle := lipfeature.FeatureBundle{
			SchemaVersion: 2,
			PlaneSet:      planes,
		}
		cs := lipfeature.NewContributionSet()
		err := ContributeBundle(cs, "contrib-bad-2", bundle)
		require.Error(t, err, "non-empty bundle with schema 2 must be rejected")
		require.Contains(t, err.Error(), "contrib-bad-2", "error must identify the responsible contributor")
		require.Equal(t, emptyFrozen, cs.Freeze())
	})

	t.Run("DirectContribute_Empty_Schema2", func(t *testing.T) {
		t.Parallel()
		emptyBundle := lipfeature.FeatureBundle{
			SchemaVersion: 2,
		}
		cs := lipfeature.NewContributionSet()
		err := ContributeBundle(cs, "contrib-empty-2", emptyBundle)
		require.Error(t, err, "empty bundle with unsupported schema 2 must be rejected")
		require.Contains(t, err.Error(), "contrib-empty-2")
		require.Equal(t, emptyFrozen, cs.Freeze())
	})

	t.Run("DirectContribute_NonEmpty_NegativeSchema", func(t *testing.T) {
		t.Parallel()
		planes := makeSchemaTestPlaneSet(t, "hook-bad-neg")
		bundle := lipfeature.FeatureBundle{
			SchemaVersion: -1,
			PlaneSet:      planes,
		}
		cs := lipfeature.NewContributionSet()
		err := ContributeBundle(cs, "contrib-bad-neg", bundle)
		require.Error(t, err, "non-empty bundle with negative schema must be rejected")
		require.Contains(t, err.Error(), "contrib-bad-neg")
		require.Equal(t, emptyFrozen, cs.Freeze())
	})

	t.Run("DirectContribute_Empty_NegativeSchema", func(t *testing.T) {
		t.Parallel()
		emptyBundle := lipfeature.FeatureBundle{
			SchemaVersion: -1,
		}
		cs := lipfeature.NewContributionSet()
		err := ContributeBundle(cs, "contrib-empty-neg", emptyBundle)
		require.Error(t, err, "empty bundle with negative schema must be rejected")
		require.Contains(t, err.Error(), "contrib-empty-neg")
		require.Equal(t, emptyFrozen, cs.Freeze())
	})

	t.Run("MergeBundlesGenerated_NonEmpty_Schema2", func(t *testing.T) {
		t.Parallel()
		planes := makeSchemaTestPlaneSet(t, "hook-bad-2")
		bundle := lipfeature.FeatureBundle{
			SchemaVersion: 2,
			PlaneSet:      planes,
		}
		gen, err := MergeBundlesGenerated(bundle)
		require.Error(t, err, "MergeBundlesGenerated with schema 2 bundle must fail")
		require.Contains(t, err.Error(), "bundle-0")
		require.True(t, gen.Frozen.IsZero())
		require.Empty(t, gen.Lifecycles)
	})

	t.Run("MergeBundlesGenerated_Empty_Schema2", func(t *testing.T) {
		t.Parallel()
		emptyBundle := lipfeature.FeatureBundle{
			SchemaVersion: 2,
		}
		gen, err := MergeBundlesGenerated(emptyBundle)
		require.Error(t, err, "MergeBundlesGenerated with empty schema 2 bundle must fail")
		require.Contains(t, err.Error(), "bundle-0")
		require.True(t, gen.Frozen.IsZero())
		require.Empty(t, gen.Lifecycles)
	})

	t.Run("MergeBundlesGenerated_NonEmpty_NegativeSchema", func(t *testing.T) {
		t.Parallel()
		planes := makeSchemaTestPlaneSet(t, "hook-bad-neg")
		bundle := lipfeature.FeatureBundle{
			SchemaVersion: -1,
			PlaneSet:      planes,
		}
		gen, err := MergeBundlesGenerated(bundle)
		require.Error(t, err, "MergeBundlesGenerated with negative schema bundle must fail")
		require.Contains(t, err.Error(), "bundle-0")
		require.True(t, gen.Frozen.IsZero())
		require.Empty(t, gen.Lifecycles)
	})

	t.Run("MergeBundlesGenerated_Empty_NegativeSchema", func(t *testing.T) {
		t.Parallel()
		emptyBundle := lipfeature.FeatureBundle{
			SchemaVersion: -1,
		}
		gen, err := MergeBundlesGenerated(emptyBundle)
		require.Error(t, err, "MergeBundlesGenerated with empty negative schema bundle must fail")
		require.Contains(t, err.Error(), "bundle-0")
		require.True(t, gen.Frozen.IsZero())
		require.Empty(t, gen.Lifecycles)
	})
}

// TestFeatureBundle_SchemaNegotiation_LifecycleOnlySchema0AndUnsupportedRejected characterizes Requirement 2.5, 2.6 (Task 1.2, Task 2.2):
// If a lifecycle-only bundle declares the compatibility zero version or an unsupported version,
// the feature composition system shall reject it before publishing any lifecycle.
func TestFeatureBundle_SchemaNegotiation_LifecycleOnlySchema0AndUnsupportedRejected(t *testing.T) {
	t.Parallel()

	emptyFrozen := lipfeature.NewContributionSet().Freeze()

	t.Run("DirectContribute_LifecycleOnly_Schema0", func(t *testing.T) {
		t.Parallel()
		bundle := lipfeature.FeatureBundle{
			SchemaVersion: 0,
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "orphan-0"}},
		}
		cs := lipfeature.NewContributionSet()
		err := ContributeBundle(cs, "contrib-lc-0", bundle)
		require.Error(t, err, "lifecycle-only bundle with schema 0 must be rejected")
		require.Contains(t, err.Error(), "contrib-lc-0", "error must identify the responsible contributor")
		require.Equal(t, emptyFrozen, cs.Freeze())
	})

	t.Run("DirectContribute_LifecycleOnly_Schema2", func(t *testing.T) {
		t.Parallel()
		bundle := lipfeature.FeatureBundle{
			SchemaVersion: 2,
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "orphan-2"}},
		}
		cs := lipfeature.NewContributionSet()
		err := ContributeBundle(cs, "contrib-lc-2", bundle)
		require.Error(t, err, "lifecycle-only bundle with schema 2 must be rejected")
		require.Contains(t, err.Error(), "contrib-lc-2", "error must identify the responsible contributor")
		require.Equal(t, emptyFrozen, cs.Freeze())
	})

	t.Run("DirectContribute_LifecycleOnly_NegativeSchema", func(t *testing.T) {
		t.Parallel()
		bundle := lipfeature.FeatureBundle{
			SchemaVersion: -1,
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "orphan-neg"}},
		}
		cs := lipfeature.NewContributionSet()
		err := ContributeBundle(cs, "contrib-lc-neg", bundle)
		require.Error(t, err, "lifecycle-only bundle with negative schema must be rejected")
		require.Contains(t, err.Error(), "contrib-lc-neg", "error must identify the responsible contributor")
		require.Equal(t, emptyFrozen, cs.Freeze())
	})

	t.Run("MergeBundlesGenerated_LifecycleOnly_Schema0", func(t *testing.T) {
		t.Parallel()
		bundle := lipfeature.FeatureBundle{
			SchemaVersion: 0,
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "orphan-0"}},
		}
		gen, err := MergeBundlesGenerated(bundle)
		require.Error(t, err, "MergeBundlesGenerated with lifecycle-only schema 0 bundle must fail")
		require.Contains(t, err.Error(), "bundle-0")
		require.True(t, gen.Frozen.IsZero())
		require.Empty(t, gen.Lifecycles, "no lifecycles should be published when bundle schema is invalid")
	})

	t.Run("MergeBundlesGenerated_LifecycleOnly_Schema2", func(t *testing.T) {
		t.Parallel()
		bundle := lipfeature.FeatureBundle{
			SchemaVersion: 2,
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "orphan-2"}},
		}
		gen, err := MergeBundlesGenerated(bundle)
		require.Error(t, err, "MergeBundlesGenerated with lifecycle-only schema 2 bundle must fail")
		require.Contains(t, err.Error(), "bundle-0")
		require.True(t, gen.Frozen.IsZero())
		require.Empty(t, gen.Lifecycles, "no lifecycles should be published when bundle schema is invalid")
	})

	t.Run("MergeBundlesGenerated_LifecycleOnly_NegativeSchema", func(t *testing.T) {
		t.Parallel()
		bundle := lipfeature.FeatureBundle{
			SchemaVersion: -1,
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "orphan-neg"}},
		}
		gen, err := MergeBundlesGenerated(bundle)
		require.Error(t, err, "MergeBundlesGenerated with lifecycle-only negative schema bundle must fail")
		require.Contains(t, err.Error(), "bundle-0")
		require.True(t, gen.Frozen.IsZero())
		require.Empty(t, gen.Lifecycles, "no lifecycles should be published when bundle schema is invalid")
	})
}

// TestFeatureBundle_ContributeBundle_FailBeforeMutate_AttributedError characterizes Requirement 2.8 (Task 1.2, Task 2.2):
// Direct ContributeBundle failure leaves a pre-populated ContributionSet byte/behavior-equivalent
// and returns an error attributed/wrapped with contributor identity.
func TestFeatureBundle_ContributeBundle_FailBeforeMutate_AttributedError(t *testing.T) {
	t.Parallel()

	setupPrepopulatedSet := func(t *testing.T) *lipfeature.ContributionSet {
		t.Helper()
		cs := lipfeature.NewContributionSet()
		require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, "init", []sdkhooks.SubmitHook{schemaTestSubmitHook{tag: "hook-init"}}))
		require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneSessionOpeners, "init", []session.Opener{schemaTestOpener{tag: "opener-init"}}))
		require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "init", 2048))
		return cs
	}

	assertEquivalence := func(t *testing.T, frozenBefore, frozenAfter lipfeature.FrozenPlaneSet) {
		t.Helper()
		require.Equal(t, frozenBefore, frozenAfter, "pre-populated ContributionSet must remain byte/behavior-equivalent on error")

		hooks := lipfeature.Get(frozenAfter, lipfeature.PlaneSubmitHooks)
		require.Len(t, hooks, 1)
		require.Equal(t, "hook-init", hooks[0].ID())

		openers := lipfeature.Get(frozenAfter, lipfeature.PlaneSessionOpeners)
		require.Len(t, openers, 1)
		require.Equal(t, "opener-init", openers[0].ID())

		capBytes := lipfeature.Get(frozenAfter, lipfeature.PlaneToolCallFinalizationMaxArgsBytes)
		require.Equal(t, 2048, capBytes)
	}

	t.Run("NonEmptySchema0", func(t *testing.T) {
		t.Parallel()
		cs := setupPrepopulatedSet(t)
		frozenBefore := cs.Freeze()

		malformed := lipfeature.FeatureBundle{
			SchemaVersion: 0,
			PlaneSet:      makeSchemaTestPlaneSet(t, "malformed-incoming-0"),
		}

		err := ContributeBundle(cs, "failing-contributor-0", malformed)
		require.Error(t, err, "ContributeBundle must return error for malformed bundle")
		require.Contains(t, err.Error(), "failing-contributor-0", "error must be attributed to contributor")
		assertEquivalence(t, frozenBefore, cs.Freeze())
	})

	t.Run("NonEmptySchema2", func(t *testing.T) {
		t.Parallel()
		cs := setupPrepopulatedSet(t)
		frozenBefore := cs.Freeze()

		malformed := lipfeature.FeatureBundle{
			SchemaVersion: 2,
			PlaneSet:      makeSchemaTestPlaneSet(t, "malformed-incoming-2"),
		}

		err := ContributeBundle(cs, "failing-contributor-2", malformed)
		require.Error(t, err, "ContributeBundle must return error for malformed schema 2 bundle")
		require.Contains(t, err.Error(), "failing-contributor-2", "error must be attributed to contributor")
		assertEquivalence(t, frozenBefore, cs.Freeze())
	})

	t.Run("LifecycleOnlySchema0", func(t *testing.T) {
		t.Parallel()
		cs := setupPrepopulatedSet(t)
		frozenBefore := cs.Freeze()

		malformed := lipfeature.FeatureBundle{
			SchemaVersion: 0,
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "orphan-lc"}},
		}

		err := ContributeBundle(cs, "failing-contributor-lc", malformed)
		require.Error(t, err, "ContributeBundle must return error for lifecycle-only schema 0 bundle")
		require.Contains(t, err.Error(), "failing-contributor-lc", "error must be attributed to contributor")
		assertEquivalence(t, frozenBefore, cs.Freeze())
	})
}

// TestFeatureBundle_MergeBundlesGenerated_FailureDiscardsAllCandidateAndLifecycles characterizes Requirement 4.3 (Task 1.2, Task 2.2):
// MergeBundlesGenerated failure returns zero GeneratedMergeSurface and publishes no lifecycle.
func TestFeatureBundle_MergeBundlesGenerated_FailureDiscardsAllCandidateAndLifecycles(t *testing.T) {
	t.Parallel()

	validBundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		PlaneSet:      makeSchemaTestPlaneSet(t, "valid-1"),
		Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-valid-1"}},
	}

	t.Run("ValidThenMalformedSchema0", func(t *testing.T) {
		t.Parallel()
		malformedBundle0 := lipfeature.FeatureBundle{
			SchemaVersion: 0,
			PlaneSet:      makeSchemaTestPlaneSet(t, "malformed-2"),
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-malformed-2"}},
		}

		gen, err := MergeBundlesGenerated(validBundle, malformedBundle0)
		require.Error(t, err, "MergeBundlesGenerated must fail when second bundle is malformed schema 0")
		require.Contains(t, err.Error(), "bundle-1", "error must be attributed to second bundle (bundle-1)")
		require.Equal(t, GeneratedMergeSurface{}, gen, "candidate must be completely discarded on error")
		require.Empty(t, gen.Lifecycles, "no lifecycles must be published on failure")
	})

	t.Run("ValidThenMalformedSchema2", func(t *testing.T) {
		t.Parallel()
		malformedBundle2 := lipfeature.FeatureBundle{
			SchemaVersion: 2,
			PlaneSet:      makeSchemaTestPlaneSet(t, "malformed-schema-2"),
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-malformed-schema-2"}},
		}

		gen, err := MergeBundlesGenerated(validBundle, malformedBundle2)
		require.Error(t, err, "MergeBundlesGenerated must fail when second bundle is malformed schema 2")
		require.Contains(t, err.Error(), "bundle-1", "error must be attributed to second bundle (bundle-1)")
		require.Equal(t, GeneratedMergeSurface{}, gen, "candidate must be completely discarded on error")
		require.Empty(t, gen.Lifecycles, "no lifecycles must be published on failure")
	})

	t.Run("ValidThenMalformedLifecycleOnlySchema0", func(t *testing.T) {
		t.Parallel()
		malformedLcBundle0 := lipfeature.FeatureBundle{
			SchemaVersion: 0,
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-malformed-lc-0"}},
		}

		gen, err := MergeBundlesGenerated(validBundle, malformedLcBundle0)
		require.Error(t, err, "MergeBundlesGenerated must fail when second bundle is lifecycle-only schema 0")
		require.Contains(t, err.Error(), "bundle-1", "error must be attributed to second bundle (bundle-1)")
		require.Equal(t, GeneratedMergeSurface{}, gen, "candidate must be completely discarded on error")
		require.Empty(t, gen.Lifecycles, "no lifecycles must be published on failure")
	})
}

// TestFeatureBundle_RegistryAndHostMerge_RejectsMalformedBundle characterizes Requirement 2.7, 2.8, 4.3 (Task 1.2, Task 2.2):
// Fake/generalized FeatureBundleRegistry returning a malformed bundle is rejected through
// MergeFeatureSurfaceGenerated and MergeFeatureSurfacesWithHost, including extra/candidate bundle.
func TestFeatureBundle_RegistryAndHostMerge_RejectsMalformedBundle(t *testing.T) {
	t.Parallel()

	malformedBundle0 := lipfeature.FeatureBundle{
		SchemaVersion: 0,
		PlaneSet:      makeSchemaTestPlaneSet(t, "reg-bad-0"),
		Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-reg-bad"}},
	}

	fakeReg := fakeFeatureBundleRegistry{
		buildFn: func(factoryKey string, _ yaml.Node) (lipfeature.FeatureBundle, error) {
			return malformedBundle0, nil
		},
	}

	validBundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		PlaneSet:      makeSchemaTestPlaneSet(t, "reg-good"),
		Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-reg-good"}},
	}

	validReg := fakeFeatureBundleRegistry{
		buildFn: func(factoryKey string, _ yaml.Node) (lipfeature.FeatureBundle, error) {
			return validBundle, nil
		},
	}

	t.Run("RegistryGenerated_WithRegistrationID", func(t *testing.T) {
		t.Parallel()
		regs := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "reg-feat-bad", FactoryKind: "bad-plugin", Enabled: true},
		}

		genReg, errReg := MergeFeatureSurfaceGenerated(fakeReg, regs)
		require.Error(t, errReg, "MergeFeatureSurfaceGenerated must reject malformed bundle from registry")
		require.Contains(t, errReg.Error(), "reg-feat-bad", "error must be attributed to registration ID")
		require.True(t, genReg.Frozen.IsZero())
		require.Empty(t, genReg.Lifecycles)
	})

	t.Run("RegistryGenerated_WithFactoryKeyFallback", func(t *testing.T) {
		t.Parallel()
		regs := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "", FactoryKind: "bad-factory-fallback", Enabled: true},
		}

		genReg, errReg := MergeFeatureSurfaceGenerated(fakeReg, regs)
		require.Error(t, errReg, "MergeFeatureSurfaceGenerated must reject malformed bundle with factory key fallback")
		require.Contains(t, errReg.Error(), "bad-factory-fallback", "error must fall back to factory key for attribution")
		require.True(t, genReg.Frozen.IsZero())
		require.Empty(t, genReg.Lifecycles)
	})

	t.Run("HostMerge_RegistryWithRegistrationID", func(t *testing.T) {
		t.Parallel()
		regs := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "host-reg-bad", FactoryKind: "bad-plugin", Enabled: true},
		}

		_, gHost, errHost := MergeFeatureSurfacesWithHost(fakeReg, regs, HostContributions{})
		require.Error(t, errHost, "MergeFeatureSurfacesWithHost must reject malformed bundle from registry")
		require.Contains(t, errHost.Error(), "host-reg-bad", "error must be attributed to registration ID")
		require.True(t, gHost.Frozen.IsZero())
		require.Empty(t, gHost.Lifecycles)
	})

	t.Run("HostMerge_RegistryWithFactoryKeyFallback", func(t *testing.T) {
		t.Parallel()
		regs := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "", FactoryKind: "host-factory-fallback", Enabled: true},
		}

		_, gHost, errHost := MergeFeatureSurfacesWithHost(fakeReg, regs, HostContributions{})
		require.Error(t, errHost, "MergeFeatureSurfacesWithHost must reject malformed bundle with factory key fallback")
		require.Contains(t, errHost.Error(), "host-factory-fallback", "error must fall back to factory key for attribution")
		require.True(t, gHost.Frozen.IsZero())
		require.Empty(t, gHost.Lifecycles)
	})

	t.Run("HostMerge_ExtraCandidateMalformed_Index0", func(t *testing.T) {
		t.Parallel()
		regsValid := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "reg-feat-good", FactoryKind: "good-plugin", Enabled: true},
		}

		malformedExtra0 := lipfeature.FeatureBundle{
			SchemaVersion: 2,
			PlaneSet:      makeSchemaTestPlaneSet(t, "extra-bad-2"),
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-extra-bad"}},
		}

		_, gExtra, errExtra := MergeFeatureSurfacesWithHost(validReg, regsValid, HostContributions{}, malformedExtra0)
		require.Error(t, errExtra, "MergeFeatureSurfacesWithHost must reject malformed extra candidate bundle at index 0")
		require.Contains(t, errExtra.Error(), "candidate-feature-0", "error must be attributed to candidate-feature-0")
		require.True(t, gExtra.Frozen.IsZero())
		require.Empty(t, gExtra.Lifecycles)
	})

	t.Run("HostMerge_ExtraCandidateMalformed_Index1", func(t *testing.T) {
		t.Parallel()
		regsValid := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "reg-feat-good", FactoryKind: "good-plugin", Enabled: true},
		}

		validExtra0 := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			PlaneSet:      makeSchemaTestPlaneSet(t, "extra-good-0"),
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-extra-good"}},
		}
		malformedExtra1 := lipfeature.FeatureBundle{
			SchemaVersion: 0,
			PlaneSet:      makeSchemaTestPlaneSet(t, "extra-bad-1"),
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-extra-bad-1"}},
		}

		_, gExtra, errExtra := MergeFeatureSurfacesWithHost(validReg, regsValid, HostContributions{}, validExtra0, malformedExtra1)
		require.Error(t, errExtra, "MergeFeatureSurfacesWithHost must reject malformed extra candidate bundle at index 1")
		require.Contains(t, errExtra.Error(), "candidate-feature-1", "error must be attributed to candidate-feature-1")
		require.True(t, gExtra.Frozen.IsZero())
		require.Empty(t, gExtra.Lifecycles)
	})
}

// TestFeatureBundle_MergeFeatureSurface_RejectsMalformedRegistryBundle characterizes Requirement 2.7, 2.8, 4.3 (Task 2.2):
// MergeFeatureSurface and MergeFeatureSurfaceViaGenerated validate registry bundles transactionally with normalized
// registration contributor attribution and return zero MergedFeatureSurface on error without building bundles twice.
func TestFeatureBundle_MergeFeatureSurface_RejectsMalformedRegistryBundle(t *testing.T) {
	t.Parallel()

	malformedBundle0 := lipfeature.FeatureBundle{
		SchemaVersion: 0,
		PlaneSet:      makeSchemaTestPlaneSet(t, "reg-bad-0"),
		Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-reg-bad"}},
	}
	malformedBundle2 := lipfeature.FeatureBundle{
		SchemaVersion: 2,
		PlaneSet:      makeSchemaTestPlaneSet(t, "reg-bad-2"),
		Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-reg-bad-2"}},
	}
	malformedLcBundle := lipfeature.FeatureBundle{
		SchemaVersion: 0,
		Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-reg-bad-lc"}},
	}

	validBundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		PlaneSet:      makeSchemaTestPlaneSet(t, "reg-good"),
		Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-reg-good"}},
	}

	t.Run("MergeFeatureSurface_RegistrationID_Schema0", func(t *testing.T) {
		t.Parallel()
		reg := fakeFeatureBundleRegistry{
			buildFn: func(factoryKey string, _ yaml.Node) (lipfeature.FeatureBundle, error) {
				return malformedBundle0, nil
			},
		}
		regs := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "reg-feat-bad0", FactoryKind: "bad-plugin", Enabled: true},
		}

		m, err := MergeFeatureSurface(reg, regs)
		require.Error(t, err, "MergeFeatureSurface must reject malformed bundle from registry")
		require.Contains(t, err.Error(), "reg-feat-bad0", "error must be attributed to registration ID")
		require.Equal(t, MergedFeatureSurface{}, m, "returned MergedFeatureSurface must be zero value on failure")
		require.Empty(t, m.Lifecycles, "no lifecycles must be published on failure")
	})

	t.Run("MergeFeatureSurface_RegistrationID_Schema2", func(t *testing.T) {
		t.Parallel()
		reg := fakeFeatureBundleRegistry{
			buildFn: func(factoryKey string, _ yaml.Node) (lipfeature.FeatureBundle, error) {
				return malformedBundle2, nil
			},
		}
		regs := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "reg-feat-bad2", FactoryKind: "bad-plugin-2", Enabled: true},
		}

		m, err := MergeFeatureSurface(reg, regs)
		require.Error(t, err, "MergeFeatureSurface must reject schema 2 bundle from registry")
		require.Contains(t, err.Error(), "reg-feat-bad2", "error must be attributed to registration ID")
		require.Equal(t, MergedFeatureSurface{}, m)
		require.Empty(t, m.Lifecycles)
	})

	t.Run("MergeFeatureSurface_RegistrationID_LifecycleOnlySchema0", func(t *testing.T) {
		t.Parallel()
		reg := fakeFeatureBundleRegistry{
			buildFn: func(factoryKey string, _ yaml.Node) (lipfeature.FeatureBundle, error) {
				return malformedLcBundle, nil
			},
		}
		regs := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "reg-feat-bad-lc", FactoryKind: "bad-plugin-lc", Enabled: true},
		}

		m, err := MergeFeatureSurface(reg, regs)
		require.Error(t, err, "MergeFeatureSurface must reject lifecycle-only schema 0 bundle from registry")
		require.Contains(t, err.Error(), "reg-feat-bad-lc", "error must be attributed to registration ID")
		require.Equal(t, MergedFeatureSurface{}, m)
		require.Empty(t, m.Lifecycles)
	})

	t.Run("MergeFeatureSurface_FactoryKeyFallback", func(t *testing.T) {
		t.Parallel()
		reg := fakeFeatureBundleRegistry{
			buildFn: func(factoryKey string, _ yaml.Node) (lipfeature.FeatureBundle, error) {
				return malformedBundle0, nil
			},
		}
		regs := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "", FactoryKind: "fallback-factory-key", Enabled: true},
		}

		m, err := MergeFeatureSurface(reg, regs)
		require.Error(t, err, "MergeFeatureSurface must reject malformed bundle with factory key fallback")
		require.Contains(t, err.Error(), "fallback-factory-key", "error must fall back to factory key for attribution")
		require.Equal(t, MergedFeatureSurface{}, m)
		require.Empty(t, m.Lifecycles)
	})

	t.Run("MergeFeatureSurface_ValidThenMalformed_TransactionalRollback", func(t *testing.T) {
		t.Parallel()
		reg := fakeFeatureBundleRegistry{
			buildFn: func(factoryKey string, _ yaml.Node) (lipfeature.FeatureBundle, error) {
				if factoryKey == "good-plugin" {
					return validBundle, nil
				}
				return malformedBundle0, nil
			},
		}
		regs := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "first-good", FactoryKind: "good-plugin", Enabled: true},
			{Kind: lipsdk.PluginKindFeature, ID: "second-bad", FactoryKind: "bad-plugin", Enabled: true},
		}

		m, err := MergeFeatureSurface(reg, regs)
		require.Error(t, err, "MergeFeatureSurface must fail when second bundle is malformed")
		require.Contains(t, err.Error(), "second-bad", "error must identify second registration")
		require.Equal(t, MergedFeatureSurface{}, m, "returned surface must be zero value, discarding first bundle lifecycles")
		require.Empty(t, m.Lifecycles)
	})

	t.Run("MergeFeatureSurface_SingleBuildPerRegistration", func(t *testing.T) {
		t.Parallel()
		buildCounts := make(map[string]int)
		reg := fakeFeatureBundleRegistry{
			buildFn: func(factoryKey string, _ yaml.Node) (lipfeature.FeatureBundle, error) {
				buildCounts[factoryKey]++
				return validBundle, nil
			},
		}
		regs := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "f1", FactoryKind: "factory-1", Enabled: true},
			{Kind: lipsdk.PluginKindFeature, ID: "f2", FactoryKind: "factory-2", Enabled: false},
			{Kind: lipsdk.PluginKindFeature, ID: "f3", FactoryKind: "factory-3", Enabled: true},
		}

		m, err := MergeFeatureSurface(reg, regs)
		require.NoError(t, err)
		require.Len(t, m.Lifecycles, 2)
		require.Equal(t, 1, buildCounts["factory-1"], "factory-1 must be built exactly once")
		require.Equal(t, 0, buildCounts["factory-2"], "disabled factory-2 must not be built")
		require.Equal(t, 1, buildCounts["factory-3"], "factory-3 must be built exactly once")
	})

	t.Run("MergeFeatureSurfaceViaGenerated_RejectsMalformed", func(t *testing.T) {
		t.Parallel()
		reg := fakeFeatureBundleRegistry{
			buildFn: func(factoryKey string, _ yaml.Node) (lipfeature.FeatureBundle, error) {
				return malformedBundle0, nil
			},
		}
		regs := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "via-gen-bad", FactoryKind: "bad-plugin", Enabled: true},
		}

		m, err := MergeFeatureSurfaceViaGenerated(reg, regs)
		require.Error(t, err)
		require.Contains(t, err.Error(), "via-gen-bad")
		require.Equal(t, MergedFeatureSurface{}, m)
		require.Empty(t, m.Lifecycles)
	})
}

// TestFeatureBundle_MergeBundlesChecked_RejectsMalformedBundles characterizes Requirement 2.4, 2.6, 4.3 (Task 2.2):
// MergeBundlesChecked rejects malformed bundles and returns zero MergedFeatureSurface on failure.
func TestFeatureBundle_MergeBundlesChecked_RejectsMalformedBundles(t *testing.T) {
	t.Parallel()

	validBundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		PlaneSet:      makeSchemaTestPlaneSet(t, "checked-good"),
		Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-checked-good"}},
	}

	t.Run("SingleMalformedSchema0", func(t *testing.T) {
		t.Parallel()
		malformed := lipfeature.FeatureBundle{
			SchemaVersion: 0,
			PlaneSet:      makeSchemaTestPlaneSet(t, "bad-0"),
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-bad-0"}},
		}

		m, err := MergeBundlesChecked(malformed)
		require.Error(t, err, "MergeBundlesChecked must reject non-empty schema 0 bundle")
		require.Contains(t, err.Error(), "bundle-0", "error must be attributed to bundle-0")
		require.Equal(t, MergedFeatureSurface{}, m, "returned surface must be zero value")
		require.Empty(t, m.Lifecycles)
	})

	t.Run("SingleMalformedSchema2", func(t *testing.T) {
		t.Parallel()
		malformed := lipfeature.FeatureBundle{
			SchemaVersion: 2,
			PlaneSet:      makeSchemaTestPlaneSet(t, "bad-2"),
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-bad-2"}},
		}

		m, err := MergeBundlesChecked(malformed)
		require.Error(t, err, "MergeBundlesChecked must reject schema 2 bundle")
		require.Contains(t, err.Error(), "bundle-0")
		require.Equal(t, MergedFeatureSurface{}, m)
		require.Empty(t, m.Lifecycles)
	})

	t.Run("SingleMalformedLifecycleOnlySchema0", func(t *testing.T) {
		t.Parallel()
		malformed := lipfeature.FeatureBundle{
			SchemaVersion: 0,
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-bad-lc"}},
		}

		m, err := MergeBundlesChecked(malformed)
		require.Error(t, err, "MergeBundlesChecked must reject lifecycle-only schema 0 bundle")
		require.Contains(t, err.Error(), "bundle-0")
		require.Equal(t, MergedFeatureSurface{}, m)
		require.Empty(t, m.Lifecycles)
	})

	t.Run("ValidThenMalformed_TransactionalRollback", func(t *testing.T) {
		t.Parallel()
		malformed := lipfeature.FeatureBundle{
			SchemaVersion: 0,
			PlaneSet:      makeSchemaTestPlaneSet(t, "bad-1"),
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-bad-1"}},
		}

		m, err := MergeBundlesChecked(validBundle, malformed)
		require.Error(t, err, "MergeBundlesChecked must reject second malformed bundle")
		require.Contains(t, err.Error(), "bundle-1", "error must be attributed to bundle-1")
		require.Equal(t, MergedFeatureSurface{}, m, "first bundle's lifecycles must be discarded on error")
		require.Empty(t, m.Lifecycles)
	})
}

// TestFeatureBundle_MergedFeatureSurfaceAppend_RejectsMalformedSchema characterizes Requirement 2.4, 2.6, 2.8 (Task 2.2):
// Direct (m *MergedFeatureSurface).Append(b) validates bundle schema and fails before mutating m.Lifecycles.
func TestFeatureBundle_MergedFeatureSurfaceAppend_RejectsMalformedSchema(t *testing.T) {
	t.Parallel()

	t.Run("NilReceiver_ReturnsError", func(t *testing.T) {
		t.Parallel()
		var nilSurface *MergedFeatureSurface
		validBundle := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-ok"}},
		}
		err := nilSurface.Append(validBundle)
		require.Error(t, err, "Append on nil receiver must return error")
		require.Equal(t, "featurebundle: nil merged feature surface", err.Error())
	})

	t.Run("NonEmptySchema0_FailBeforeMutate", func(t *testing.T) {
		t.Parallel()
		surface := MergedFeatureSurface{
			Lifecycles: []lipplugin.Lifecycle{schemaTestLifecycle{tag: "initial-life"}},
		}
		malformed := lipfeature.FeatureBundle{
			SchemaVersion: 0,
			PlaneSet:      makeSchemaTestPlaneSet(t, "bad-append-0"),
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "bad-append-life"}},
		}

		err := surface.Append(malformed)
		require.Error(t, err, "Append must reject non-empty schema 0 bundle")
		require.Contains(t, err.Error(), "legacy-append", "error must wrap stable contributor identity")
		require.Len(t, surface.Lifecycles, 1, "surface.Lifecycles must not be mutated on failure")
		require.Equal(t, "initial-life", surface.Lifecycles[0].(schemaTestLifecycle).tag)
	})

	t.Run("Schema2_FailBeforeMutate", func(t *testing.T) {
		t.Parallel()
		surface := MergedFeatureSurface{
			Lifecycles: []lipplugin.Lifecycle{schemaTestLifecycle{tag: "initial-life"}},
		}
		malformed := lipfeature.FeatureBundle{
			SchemaVersion: 2,
			PlaneSet:      makeSchemaTestPlaneSet(t, "bad-append-2"),
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "bad-append-life-2"}},
		}

		err := surface.Append(malformed)
		require.Error(t, err, "Append must reject schema 2 bundle")
		require.Contains(t, err.Error(), "legacy-append")
		require.Len(t, surface.Lifecycles, 1)
		require.Equal(t, "initial-life", surface.Lifecycles[0].(schemaTestLifecycle).tag)
	})

	t.Run("LifecycleOnlySchema0_FailBeforeMutate", func(t *testing.T) {
		t.Parallel()
		surface := MergedFeatureSurface{
			Lifecycles: []lipplugin.Lifecycle{schemaTestLifecycle{tag: "initial-life"}},
		}
		malformed := lipfeature.FeatureBundle{
			SchemaVersion: 0,
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "bad-append-lc"}},
		}

		err := surface.Append(malformed)
		require.Error(t, err, "Append must reject lifecycle-only schema 0 bundle")
		require.Contains(t, err.Error(), "legacy-append")
		require.Len(t, surface.Lifecycles, 1)
		require.Equal(t, "initial-life", surface.Lifecycles[0].(schemaTestLifecycle).tag)
	})

	t.Run("ValidBundle_AppendsLifecycles", func(t *testing.T) {
		t.Parallel()
		surface := MergedFeatureSurface{
			Lifecycles: []lipplugin.Lifecycle{schemaTestLifecycle{tag: "initial-life"}},
		}
		valid := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			PlaneSet:      makeSchemaTestPlaneSet(t, "good-append"),
			Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "second-life"}},
		}

		err := surface.Append(valid)
		require.NoError(t, err, "Append must accept valid V1 bundle")
		require.Len(t, surface.Lifecycles, 2)
		require.Equal(t, "initial-life", surface.Lifecycles[0].(schemaTestLifecycle).tag)
		require.Equal(t, "second-life", surface.Lifecycles[1].(schemaTestLifecycle).tag)
	})
}
