package featurebundle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/stretchr/testify/require"
)

// --- Stubs for scalar and exclusive characterization ---

type charStubSubmitHook struct{ tag string }

func (h charStubSubmitHook) ID() string                      { return h.tag }
func (charStubSubmitHook) Order() int                        { return 0 }
func (charStubSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubSubmitHook) Handle(context.Context, *lipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type charStubTerminalProvider struct {
	tag string
}

func (p charStubTerminalProvider) ID() string { return p.tag }

func (charStubTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

type charStubBadIDTerminalProvider struct {
	badID string
}

func (p charStubBadIDTerminalProvider) ID() string { return p.badID }

func (charStubBadIDTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

type charStubPanicTerminalProvider struct{}

func (charStubPanicTerminalProvider) ID() string { panic("provider id explosion") }

func (charStubPanicTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

func testBundle(fn func(cs *lipfeature.ContributionSet)) lipfeature.FeatureBundle {
	cs := lipfeature.NewContributionSet()
	if fn != nil {
		fn(cs)
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil)
}

// --- Acceptance Criteria 1: Finalizer-Cap Merge Path ---

// TestFinalizerCap_MergePathMinReductionAndZeroSemantics pins requirement 4.4 and 1.2:
// - Positive values are min-reduced across merged bundles in registration order.
// - Zero values represent "unset" and do not reduce or set the finalizer cap.
// - Negative values are non-positive and ignored at merge time (rejected by FeatureBundle.Validate).
// - Equal positive values exhibit idempotence.
// - An empty bundle list or all-zero bundles result in 0.
func TestFinalizerCap_MergePathMinReductionAndZeroSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		bundles []lipfeature.FeatureBundle
		want    int
	}{
		{
			name: "single_positive_cap",
			bundles: []lipfeature.FeatureBundle{
				testBundle(func(cs *lipfeature.ContributionSet) {
					_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 4096)
				}),
			},
			want: 4096,
		},
		{
			name: "decreasing_sequence_min_reduction",
			bundles: []lipfeature.FeatureBundle{
				testBundle(func(cs *lipfeature.ContributionSet) {
					_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 8192)
				}),
				testBundle(func(cs *lipfeature.ContributionSet) {
					_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 4096)
				}),
				testBundle(func(cs *lipfeature.ContributionSet) {
					_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 1024)
				}),
			},
			want: 1024,
		},
		{
			name: "increasing_sequence_min_reduction",
			bundles: []lipfeature.FeatureBundle{
				testBundle(func(cs *lipfeature.ContributionSet) {
					_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 1024)
				}),
				testBundle(func(cs *lipfeature.ContributionSet) {
					_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 4096)
				}),
				testBundle(func(cs *lipfeature.ContributionSet) {
					_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 8192)
				}),
			},
			want: 1024,
		},
		{
			name: "equal_value_idempotence",
			bundles: []lipfeature.FeatureBundle{
				testBundle(func(cs *lipfeature.ContributionSet) {
					_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 2048)
				}),
				testBundle(func(cs *lipfeature.ContributionSet) {
					_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 2048)
				}),
				testBundle(func(cs *lipfeature.ContributionSet) {
					_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 2048)
				}),
			},
			want: 2048,
		},
		{
			name: "interspersed_zeros_zero_as_unset",
			bundles: []lipfeature.FeatureBundle{
				testBundle(nil),
				testBundle(func(cs *lipfeature.ContributionSet) {
					_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 4096)
				}),
				testBundle(nil),
				testBundle(func(cs *lipfeature.ContributionSet) {
					_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 1024)
				}),
				testBundle(nil),
			},
			want: 1024,
		},
		{
			name: "interspersed_negatives_ignored",
			bundles: []lipfeature.FeatureBundle{
				testBundle(nil),
				testBundle(func(cs *lipfeature.ContributionSet) {
					_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 2048)
				}),
				testBundle(nil),
			},
			want: 2048,
		},
		{
			name: "all_zero_bundles",
			bundles: []lipfeature.FeatureBundle{
				testBundle(nil),
				testBundle(nil),
			},
			want: 0,
		},
		{
			name:    "empty_bundle_list",
			bundles: nil,
			want:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gen, err := MergeBundlesGenerated(tt.bundles...)
			require.NoError(t, err)
			require.Equal(t, tt.want, lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCallFinalizationMaxArgsBytes))
		})
	}

	t.Run("step_by_step_receiver_append_mutation", func(t *testing.T) {
		t.Parallel()
		cs := lipfeature.NewContributionSet()
		require.Equal(t, 0, lipfeature.Get(cs.Freeze(), lipfeature.PlaneToolCallFinalizationMaxArgsBytes))

		// 1. First contribution sets value
		err := lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-1", 4096)
		require.NoError(t, err)
		require.Equal(t, 4096, lipfeature.Get(cs.Freeze(), lipfeature.PlaneToolCallFinalizationMaxArgsBytes))

		// 2. Larger contribution ignored (min-reduction)
		err = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-2", 8192)
		require.NoError(t, err)
		require.Equal(t, 4096, lipfeature.Get(cs.Freeze(), lipfeature.PlaneToolCallFinalizationMaxArgsBytes))

		// 3. Smaller contribution reduces value
		err = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-3", 2048)
		require.NoError(t, err)
		require.Equal(t, 2048, lipfeature.Get(cs.Freeze(), lipfeature.PlaneToolCallFinalizationMaxArgsBytes))

		// 4. Zero contribution ignored (unset)
		err = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-4", 0)
		require.NoError(t, err)
		require.Equal(t, 2048, lipfeature.Get(cs.Freeze(), lipfeature.PlaneToolCallFinalizationMaxArgsBytes))

		// 5. Negative contribution fails validation
		err = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-5", -100)
		require.Error(t, err)
		require.Equal(t, 2048, lipfeature.Get(cs.Freeze(), lipfeature.PlaneToolCallFinalizationMaxArgsBytes))
	})
}

// --- Acceptance Criteria 2: Exclusive Terminal Decision Provider ---

// TestTerminalDecision_ExclusiveOccupationAndConflictText pins requirement 1.2, 4.2, and 5.1:
// - Zero providers leaves slot nil.
// - Single valid provider occupies slot.
// - Second distinct provider fails with exact ErrExclusiveConflict wrapping and %q and %q formatting.
// - Candidate is discarded on conflict.
// - Same-provider re-contribution is characterized (returns conflict error with duplicate IDs).
// - Fail-before-mutate: invalid or conflicting provider contribution leaves candidate completely unmodified.
func TestTerminalDecision_ExclusiveOccupationAndConflictText(t *testing.T) {
	t.Parallel()

	provA := charStubTerminalProvider{tag: "alg.provider.a"}
	provB := charStubTerminalProvider{tag: "alg.provider.b"}

	t.Run("zero_providers_leaves_slot_nil", func(t *testing.T) {
		t.Parallel()
		gen, err := MergeBundlesGenerated(
			testBundle(func(cs *lipfeature.ContributionSet) {
				_ = lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, "test", []sdkhooks.SubmitHook{charStubSubmitHook{tag: "h1"}})
			}),
		)
		require.NoError(t, err)
		require.Nil(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneTerminalDecisionProvider))
	})

	t.Run("single_valid_provider_occupies_slot", func(t *testing.T) {
		t.Parallel()
		gen, err := MergeBundlesGenerated(
			testBundle(func(cs *lipfeature.ContributionSet) {
				_ = lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, "plugin-a", terminaldecision.Provider(provA))
			}),
		)
		require.NoError(t, err)
		require.Equal(t, provA, lipfeature.Get(gen.Frozen, lipfeature.PlaneTerminalDecisionProvider))
	})

	t.Run("distinct_providers_conflict_exact_text_and_type", func(t *testing.T) {
		t.Parallel()
		gen, err := MergeBundlesGenerated(
			testBundle(func(cs *lipfeature.ContributionSet) {
				_ = lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, "plugin-a", terminaldecision.Provider(provA))
			}),
			testBundle(func(cs *lipfeature.ContributionSet) {
				_ = lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, "plugin-b", terminaldecision.Provider(provB))
			}),
		)
		require.Error(t, err)
		require.ErrorIs(t, err, lipfeature.ErrExclusiveConflict)
		require.ErrorIs(t, err, lipfeature.ErrTerminalDecisionProviderConflict)
		require.Contains(t, err.Error(), `"`+provA.ID()+`" and "`+provB.ID()+`"`)
		require.Equal(t, GeneratedMergeSurface{}, gen)
	})

	t.Run("same_provider_recontribution_characterization", func(t *testing.T) {
		t.Parallel()
		gen, err := MergeBundlesGenerated(
			testBundle(func(cs *lipfeature.ContributionSet) {
				_ = lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, "plugin-a", terminaldecision.Provider(provA))
			}),
			testBundle(func(cs *lipfeature.ContributionSet) {
				_ = lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, "plugin-b", terminaldecision.Provider(provA))
			}),
		)
		require.Error(t, err)
		require.ErrorIs(t, err, lipfeature.ErrExclusiveConflict)
		require.ErrorIs(t, err, lipfeature.ErrTerminalDecisionProviderConflict)
		require.Contains(t, err.Error(), `"`+provA.ID()+`" and "`+provA.ID()+`"`)
		require.Equal(t, GeneratedMergeSurface{}, gen)
	})

	t.Run("fail_before_mutate_on_exclusive_conflict", func(t *testing.T) {
		t.Parallel()
		gen, err := MergeBundlesGenerated(
			testBundle(func(cs *lipfeature.ContributionSet) {
				_ = lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, "plugin-a", []sdkhooks.SubmitHook{charStubSubmitHook{tag: "hook-init"}})
				_ = lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, "plugin-a", terminaldecision.Provider(provA))
			}),
			testBundle(func(cs *lipfeature.ContributionSet) {
				_ = lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, "plugin-b", []sdkhooks.SubmitHook{charStubSubmitHook{tag: "hook-new"}})
				_ = lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, "plugin-b", terminaldecision.Provider(provB))
			}),
		)
		require.Error(t, err)
		require.ErrorIs(t, err, lipfeature.ErrExclusiveConflict)
		require.Equal(t, GeneratedMergeSurface{}, gen, "candidate must not be returned when exclusive provider conflicts")
	})

	t.Run("fail_before_mutate_on_invalid_incoming_provider", func(t *testing.T) {
		t.Parallel()
		badProv := charStubBadIDTerminalProvider{badID: ""}
		cs := lipfeature.NewContributionSet()
		err := lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, "plugin-b", terminaldecision.Provider(badProv))
		require.Error(t, err)
		require.Nil(t, lipfeature.Get(cs.Freeze(), lipfeature.PlaneTerminalDecisionProvider), "candidate must not be mutated when incoming provider is invalid")
	})

	t.Run("fail_before_mutate_on_typed_nil_provider", func(t *testing.T) {
		t.Parallel()
		var typedNil *charStubTerminalProvider
		cs := lipfeature.NewContributionSet()
		err := lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, "plugin-b", terminaldecision.Provider(typedNil))
		require.Error(t, err)
		require.True(t, errors.Is(err, terminaldecision.ErrInvalidProvider))
		require.Nil(t, lipfeature.Get(cs.Freeze(), lipfeature.PlaneTerminalDecisionProvider), "candidate must not be mutated when incoming provider is typed nil")
	})

	t.Run("fail_before_mutate_on_panicking_provider", func(t *testing.T) {
		t.Parallel()
		cs := lipfeature.NewContributionSet()
		err := lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, "plugin-b", terminaldecision.Provider(charStubPanicTerminalProvider{}))
		require.Error(t, err)
		require.True(t, errors.Is(err, terminaldecision.ErrInvalidProvider))
		require.Nil(t, lipfeature.Get(cs.Freeze(), lipfeature.PlaneTerminalDecisionProvider), "candidate must not be mutated when incoming provider panics during ID()")
	})
}

// TestTerminalDecision_ProviderIdentityValidation pins valid vs invalid provider IDs.
func TestTerminalDecision_ProviderIdentityValidation(t *testing.T) {
	t.Parallel()

	validIDs := []string{
		"alg.breach.v1",
		"provider-1",
		"my_provider",
		"vendor.loop-guard.v2",
	}
	for _, id := range validIDs {
		t.Run("valid_"+id, func(t *testing.T) {
			t.Parallel()
			prov := charStubTerminalProvider{tag: id}
			cs := lipfeature.NewContributionSet()
			err := lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, "test", terminaldecision.Provider(prov))
			require.NoError(t, err)
			require.Equal(t, prov, lipfeature.Get(cs.Freeze(), lipfeature.PlaneTerminalDecisionProvider))
		})
	}

	invalidCases := []struct {
		name     string
		provider terminaldecision.Provider
	}{
		{"empty_id", charStubBadIDTerminalProvider{badID: ""}},
		{"whitespace_only_id", charStubBadIDTerminalProvider{badID: "   \t\n"}},
		{"invalid_utf8", charStubBadIDTerminalProvider{badID: "\xff\xfe\xfd"}},
		{"exceeds_max_bytes", charStubBadIDTerminalProvider{badID: strings.Repeat("x", terminaldecision.MaxProviderIDBytes+1)}},
		{"panicking_provider", charStubPanicTerminalProvider{}},
		{"typed_nil_provider", (*charStubTerminalProvider)(nil)},
	}
	for _, tc := range invalidCases {
		t.Run("invalid_"+tc.name, func(t *testing.T) {
			t.Parallel()
			cs := lipfeature.NewContributionSet()
			err := lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, "test", tc.provider)
			require.Error(t, err)
		})
	}
}
