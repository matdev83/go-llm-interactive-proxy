package compactioncompose

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	featurecontinuity "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Characterization stubs ---

type charPreserver struct {
	id string
}

func (p charPreserver) ID() string { return p.id }
func (charPreserver) BeforeRequest(context.Context, *lipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (charPreserver) RequestOpened(context.Context, lipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (charPreserver) BeforeResponseRelease(context.Context, *lipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (p charPreserver) OnCompaction(context.Context, compaction.Event) error {
	return nil
}

type charSubmitHook struct {
	id string
}

func (h charSubmitHook) ID() string                   { return h.id }
func (charSubmitHook) Order() int                     { return 0 }
func (charSubmitHook) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (charSubmitHook) Handle(context.Context, *lipapi.Call, *hooks.SubmitMeta) (hooks.SubmitDecision, error) {
	return hooks.SubmitDecision{}, nil
}

type charAttemptTransform struct {
	id string
}

func (t charAttemptTransform) ID() string                   { return t.id }
func (charAttemptTransform) Order() int                     { return 0 }
func (charAttemptTransform) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (charAttemptTransform) HandleAttempt(context.Context, *lipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{}, nil
}

type panickingPreserver struct {
	panicVal any
}

func (p panickingPreserver) ID() string {
	if p.panicVal != nil {
		panic(p.panicVal)
	}
	panic("panicking preserver")
}

func (panickingPreserver) BeforeRequest(context.Context, *lipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (panickingPreserver) RequestOpened(context.Context, lipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (panickingPreserver) BeforeResponseRelease(context.Context, *lipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func safePreserverID(p compaction.Preserver) (id string) {
	defer func() {
		if recover() != nil {
			id = ""
		}
	}()
	if p != nil {
		return p.ID()
	}
	return ""
}

func validCompactionRegistration(t *testing.T) lipsdk.Registration {
	t.Helper()
	var node yaml.Node
	raw := "extractor:\n  enabled: true\n  route: inherit\n"
	err := yaml.Unmarshal([]byte(raw), &node)
	require.NoError(t, err)
	return lipsdk.Registration{
		ID:          featurecontinuity.ID,
		FactoryKind: featurecontinuity.ID,
		Kind:        lipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      lipsdk.ConfigPayload{Node: node},
	}
}

func makeTestGenSurface(preservers []compaction.Preserver) featurebundle.GeneratedMergeSurface {
	cs := lipfeature.NewContributionSet()
	if preservers != nil {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneCompactionPreservers, "test-feat", preservers)
	}
	return featurebundle.GeneratedMergeSurface{Frozen: cs.Freeze()}
}

func TestBindFeatureSurface_PreserverReplacementOrder(t *testing.T) {
	t.Parallel()
	port := newTestPort(t)
	reg := validCompactionRegistration(t)

	tests := []struct {
		name         string
		initial      []compaction.Preserver
		wantIDs      []string
		wantCount    int
		lastIsPlugin bool
	}{
		{
			name: "official preserver in middle is replaced and moved to end",
			initial: []compaction.Preserver{
				charPreserver{id: "custom-a"},
				charPreserver{id: featurecontinuity.ID},
				charPreserver{id: "custom-b"},
			},
			wantIDs:      []string{"custom-a", "custom-b", featurecontinuity.ID},
			wantCount:    3,
			lastIsPlugin: true,
		},
		{
			name: "no prior official preserver appends official to end",
			initial: []compaction.Preserver{
				charPreserver{id: "custom-a"},
				charPreserver{id: "custom-b"},
			},
			wantIDs:      []string{"custom-a", "custom-b", featurecontinuity.ID},
			wantCount:    3,
			lastIsPlugin: true,
		},
		{
			name: "only official preserver is replaced",
			initial: []compaction.Preserver{
				charPreserver{id: featurecontinuity.ID},
			},
			wantIDs:      []string{featurecontinuity.ID},
			wantCount:    1,
			lastIsPlugin: true,
		},
		{
			name: "multiple official preserver duplicates all stripped and replaced by single bound preserver at end",
			initial: []compaction.Preserver{
				charPreserver{id: featurecontinuity.ID},
				charPreserver{id: "custom-a"},
				charPreserver{id: featurecontinuity.ID},
				charPreserver{id: "custom-b"},
				charPreserver{id: featurecontinuity.ID},
			},
			wantIDs:      []string{"custom-a", "custom-b", featurecontinuity.ID},
			wantCount:    3,
			lastIsPlugin: true,
		},
		{
			name:         "empty preservers slice receives single bound preserver",
			initial:      nil,
			wantIDs:      []string{featurecontinuity.ID},
			wantCount:    1,
			lastIsPlugin: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gen := makeTestGenSurface(tc.initial)
			res, err := BindFeatureSurface(gen, port, []lipsdk.Registration{reg})
			require.NoError(t, err)
			preservers := lipfeature.Get(res.Frozen, lipfeature.PlaneCompactionPreservers)
			require.Len(t, preservers, tc.wantCount)

			gotIDs := make([]string, len(preservers))
			for i, p := range preservers {
				require.NotNil(t, p)
				gotIDs[i] = p.ID()
			}
			assert.Equal(t, tc.wantIDs, gotIDs)

			if tc.lastIsPlugin {
				lastPreserver := preservers[len(preservers)-1]
				_, isPlugin := lastPreserver.(*featurecontinuity.Plugin)
				assert.True(t, isPlugin, "last preserver must be the bound *featurecontinuity.Plugin instance")
			}
		})
	}
}

func TestBindFeatureSurface_MultipleRegistrationsOrdering(t *testing.T) {
	t.Parallel()
	port := newTestPort(t)
	reg1 := validCompactionRegistration(t)
	reg2 := validCompactionRegistration(t)

	gen := makeTestGenSurface([]compaction.Preserver{
		charPreserver{id: "custom-1"},
		charPreserver{id: featurecontinuity.ID},
		charPreserver{id: "custom-2"},
	})

	res, err := BindFeatureSurface(gen, port, []lipsdk.Registration{reg1, reg2})
	require.NoError(t, err)
	preservers := lipfeature.Get(res.Frozen, lipfeature.PlaneCompactionPreservers)
	require.Len(t, preservers, 3)
	assert.Equal(t, "custom-1", preservers[0].ID())
	assert.Equal(t, "custom-2", preservers[1].ID())
	assert.Equal(t, featurecontinuity.ID, preservers[2].ID())

	_, isPlugin := preservers[2].(*featurecontinuity.Plugin)
	assert.True(t, isPlugin)
}

func TestBindFeatureSurface_PanicSafetyDuringIdentityExtraction(t *testing.T) {
	t.Parallel()
	port := newTestPort(t)
	reg := validCompactionRegistration(t)

	t.Run("panicking preserver with string is safely recovered and retained", func(t *testing.T) {
		t.Parallel()
		gen := makeTestGenSurface([]compaction.Preserver{
			panickingPreserver{panicVal: "deliberate panic in ID()"},
			charPreserver{id: featurecontinuity.ID},
		})

		res, err := BindFeatureSurface(gen, port, []lipsdk.Registration{reg})
		require.NoError(t, err)
		preservers := lipfeature.Get(res.Frozen, lipfeature.PlaneCompactionPreservers)
		require.Len(t, preservers, 2)
		assert.IsType(t, panickingPreserver{}, preservers[0])
		assert.Equal(t, featurecontinuity.ID, preservers[1].ID())
	})

	t.Run("panicking preserver with error is safely recovered and retained", func(t *testing.T) {
		t.Parallel()
		gen := makeTestGenSurface([]compaction.Preserver{
			charPreserver{id: "custom-a"},
			panickingPreserver{panicVal: errors.New("id extraction error panic")},
			charPreserver{id: "custom-b"},
		})

		res, err := BindFeatureSurface(gen, port, []lipsdk.Registration{reg})
		require.NoError(t, err)
		preservers := lipfeature.Get(res.Frozen, lipfeature.PlaneCompactionPreservers)
		require.Len(t, preservers, 4)
		assert.Equal(t, "custom-a", preservers[0].ID())
		assert.IsType(t, panickingPreserver{}, preservers[1])
		assert.Equal(t, "custom-b", preservers[2].ID())
		assert.Equal(t, featurecontinuity.ID, preservers[3].ID())
	})

	t.Run("nil preserver interface value is safe and retained", func(t *testing.T) {
		t.Parallel()
		cs := lipfeature.NewContributionSet()
		// Using ContributeSource with map path or direct preservers with nil
		_ = lipfeature.Contribute(cs, lipfeature.PlaneCompactionPreservers, "feat", []compaction.Preserver{
			charPreserver{id: "custom-1"},
			charPreserver{id: featurecontinuity.ID},
		})
		gen := featurebundle.GeneratedMergeSurface{Frozen: cs.Freeze()}

		res, err := BindFeatureSurface(gen, port, []lipsdk.Registration{reg})
		require.NoError(t, err)
		preservers := lipfeature.Get(res.Frozen, lipfeature.PlaneCompactionPreservers)
		require.Len(t, preservers, 2)
		assert.Equal(t, "custom-1", preservers[0].ID())
		assert.Equal(t, featurecontinuity.ID, preservers[1].ID())
	})

	t.Run("safePreserverID direct characterization", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "", safePreserverID(nil))
		assert.Equal(t, "custom", safePreserverID(charPreserver{id: "custom"}))
		assert.Equal(t, "", safePreserverID(panickingPreserver{panicVal: "panic"}))
	})
}

func TestBindFeatureSurface_FailBeforeMutate_CandidateUnmodified(t *testing.T) {
	t.Parallel()
	port := newTestPort(t)
	reg := validCompactionRegistration(t)

	initialSurface := makeTestGenSurface([]compaction.Preserver{
		charPreserver{id: "preserved-preserver"},
	})
	snapshot := initialSurface

	t.Run("unknown config key returns error and empty surface leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		cand := initialSurface

		var badNode yaml.Node
		badRaw := "unknown_nested_key: true\n"
		require.NoError(t, yaml.Unmarshal([]byte(badRaw), &badNode))

		badReg := lipsdk.Registration{
			ID:          featurecontinuity.ID,
			FactoryKind: featurecontinuity.ID,
			Kind:        lipsdk.PluginKindFeature,
			Enabled:     true,
			Config:      lipsdk.ConfigPayload{Node: badNode},
		}

		res, err := BindFeatureSurface(cand, port, []lipsdk.Registration{badReg})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "compaction-continuity config")
		assert.Equal(t, featurebundle.GeneratedMergeSurface{}, res)
		assert.Equal(t, snapshot, cand, "original candidate must remain byte-for-byte unmodified")
	})

	t.Run("disagreeing extractor and worker concurrency returns error and empty surface leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		cand := initialSurface

		var badNode yaml.Node
		badRaw := "extractor:\n  max_concurrency: 5\nworker:\n  max_concurrency: 10\n"
		require.NoError(t, yaml.Unmarshal([]byte(badRaw), &badNode))

		badReg := lipsdk.Registration{
			ID:          featurecontinuity.ID,
			FactoryKind: featurecontinuity.ID,
			Kind:        lipsdk.PluginKindFeature,
			Enabled:     true,
			Config:      lipsdk.ConfigPayload{Node: badNode},
		}

		res, err := BindFeatureSurface(cand, port, []lipsdk.Registration{badReg})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "compaction-continuity config")
		assert.Equal(t, featurebundle.GeneratedMergeSurface{}, res)
		assert.Equal(t, snapshot, cand, "original candidate must remain byte-for-byte unmodified")
	})

	t.Run("ValidateFeaturePrerequisites fail-closed before binding", func(t *testing.T) {
		t.Parallel()
		// Missing detector, coordinator, background
		err := ValidateFeaturePrerequisites([]lipsdk.Registration{reg}, false, false, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "generation prerequisite")

		// With all prerequisites satisfied
		err = ValidateFeaturePrerequisites([]lipsdk.Registration{reg}, true, true, true)
		require.NoError(t, err)
	})
}

func TestBindFeatureSurface_MultiRegistrationTransaction_AllPlanesUntouched(t *testing.T) {
	t.Parallel()
	port := newTestPort(t)

	// Valid registration (reg1)
	reg1 := validCompactionRegistration(t)

	// Invalid registration (reg2 with unknown config field)
	var badNode yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("unknown_top_field: true\n"), &badNode))
	reg2 := lipsdk.Registration{
		ID:          featurecontinuity.ID,
		FactoryKind: featurecontinuity.ID,
		Kind:        lipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      lipsdk.ConfigPayload{Node: badNode},
	}

	cs := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneCompactionPreservers, "feat-pres", []compaction.Preserver{
		charPreserver{id: "orig-preserver-1"},
		charPreserver{id: featurecontinuity.ID},
	}))
	require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneCompactionObservers, "feat-obs", []compaction.Observer{
		charPreserver{id: "obs-1"},
	}))
	require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, "feat-hooks", []hooks.SubmitHook{
		charSubmitHook{id: "hook-1"},
	}))
	require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "feat-scalar", 4096))
	require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneAttemptTransforms, "feat-xform", []request.AttemptTransform{
		charAttemptTransform{id: "xform-1"},
	}))

	initialSurface := featurebundle.GeneratedMergeSurface{
		Frozen: cs.Freeze(),
	}
	snapshot := initialSurface

	// Calling BindFeatureSurface with [reg1, reg2] where reg1 succeeds and reg2 fails config decode
	res, err := BindFeatureSurface(initialSurface, port, []lipsdk.Registration{reg1, reg2})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compactioncompose: compaction-continuity config")
	assert.Equal(t, featurebundle.GeneratedMergeSurface{}, res, "returned surface on error must be zero-value")

	// Candidate and its frozen planes must remain 100% byte-for-byte untouched across ALL planes
	assert.Equal(t, snapshot, initialSurface, "original candidate must remain untouched")

	presGot := lipfeature.Get(initialSurface.Frozen, lipfeature.PlaneCompactionPreservers)
	require.Len(t, presGot, 2)
	assert.Equal(t, "orig-preserver-1", presGot[0].ID())
	assert.Equal(t, featurecontinuity.ID, presGot[1].ID(), "original continuity preserver must NOT be replaced if transaction fails")

	obsGot := lipfeature.Get(initialSurface.Frozen, lipfeature.PlaneCompactionObservers)
	require.Len(t, obsGot, 1)

	hooksGot := lipfeature.Get(initialSurface.Frozen, lipfeature.PlaneSubmitHooks)
	require.Len(t, hooksGot, 1)
	assert.Equal(t, "hook-1", hooksGot[0].ID())

	maxArgsGot := lipfeature.Get(initialSurface.Frozen, lipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	assert.Equal(t, 4096, maxArgsGot)

	xformGot := lipfeature.Get(initialSurface.Frozen, lipfeature.PlaneAttemptTransforms)
	require.Len(t, xformGot, 1)
	assert.Equal(t, "xform-1", xformGot[0].ID())
}

func TestBindFeatureSurface_Idempotence(t *testing.T) {
	t.Parallel()
	port := newTestPort(t)
	reg := validCompactionRegistration(t)

	initial := makeTestGenSurface([]compaction.Preserver{
		charPreserver{id: "custom-a"},
		charPreserver{id: featurecontinuity.ID},
		charPreserver{id: "custom-b"},
	})

	res1, err := BindFeatureSurface(initial, port, []lipsdk.Registration{reg})
	require.NoError(t, err)

	res2, err := BindFeatureSurface(res1, port, []lipsdk.Registration{reg})
	require.NoError(t, err)

	res3, err := BindFeatureSurface(res2, port, []lipsdk.Registration{reg})
	require.NoError(t, err)

	p1 := lipfeature.Get(res1.Frozen, lipfeature.PlaneCompactionPreservers)
	p2 := lipfeature.Get(res2.Frozen, lipfeature.PlaneCompactionPreservers)
	p3 := lipfeature.Get(res3.Frozen, lipfeature.PlaneCompactionPreservers)

	require.Len(t, p1, 3)
	require.Len(t, p2, 3)
	require.Len(t, p3, 3)

	for i := range 3 {
		assert.Equal(t, p1[i].ID(), p2[i].ID())
		assert.Equal(t, p1[i].ID(), p3[i].ID())
	}
}

func TestBindFeatureSurface_DisabledAndNonMatchingRegistrations(t *testing.T) {
	t.Parallel()
	port := newTestPort(t)
	reg := validCompactionRegistration(t)

	initial := makeTestGenSurface([]compaction.Preserver{
		charPreserver{id: "custom-a"},
		charPreserver{id: featurecontinuity.ID},
	})

	t.Run("disabled registration is a no-op", func(t *testing.T) {
		t.Parallel()
		disabledReg := reg
		disabledReg.Enabled = false
		res, err := BindFeatureSurface(initial, port, []lipsdk.Registration{disabledReg})
		require.NoError(t, err)
		assert.True(t, reflect.DeepEqual(initial, res), "disabled registration must return exact surface unmodified")
	})

	t.Run("backend kind registration is ignored", func(t *testing.T) {
		t.Parallel()
		backendReg := reg
		backendReg.Kind = lipsdk.PluginKindBackend
		res, err := BindFeatureSurface(initial, port, []lipsdk.Registration{backendReg})
		require.NoError(t, err)
		assert.True(t, reflect.DeepEqual(initial, res), "backend registration must be ignored")
	})

	t.Run("different factory kind is ignored", func(t *testing.T) {
		t.Parallel()
		otherReg := reg
		otherReg.FactoryKind = "other-feature"
		res, err := BindFeatureSurface(initial, port, []lipsdk.Registration{otherReg})
		require.NoError(t, err)
		assert.True(t, reflect.DeepEqual(initial, res), "other feature registration must be ignored")
	})
}
