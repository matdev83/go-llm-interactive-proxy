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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Characterization stubs for compaction preserver ---

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
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			merged := featurebundle.MergedFeatureSurface{
				CompactionPreservers: tc.initial,
			}
			res, err := BindFeatureSurface(merged, port, []lipsdk.Registration{reg})
			require.NoError(t, err)
			require.Len(t, res.CompactionPreservers, tc.wantCount)

			gotIDs := make([]string, len(res.CompactionPreservers))
			for i, p := range res.CompactionPreservers {
				require.NotNil(t, p)
				gotIDs[i] = p.ID()
			}
			assert.Equal(t, tc.wantIDs, gotIDs)

			if tc.lastIsPlugin {
				lastPreserver := res.CompactionPreservers[len(res.CompactionPreservers)-1]
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

	merged := featurebundle.MergedFeatureSurface{
		CompactionPreservers: []compaction.Preserver{
			charPreserver{id: "custom-1"},
			charPreserver{id: featurecontinuity.ID},
			charPreserver{id: "custom-2"},
		},
	}

	res, err := BindFeatureSurface(merged, port, []lipsdk.Registration{reg1, reg2})
	require.NoError(t, err)
	require.Len(t, res.CompactionPreservers, 3)
	assert.Equal(t, "custom-1", res.CompactionPreservers[0].ID())
	assert.Equal(t, "custom-2", res.CompactionPreservers[1].ID())
	assert.Equal(t, featurecontinuity.ID, res.CompactionPreservers[2].ID())

	_, isPlugin := res.CompactionPreservers[2].(*featurecontinuity.Plugin)
	assert.True(t, isPlugin)
}

func TestBindFeatureSurface_PanicSafetyDuringIdentityExtraction(t *testing.T) {
	t.Parallel()
	port := newTestPort(t)
	reg := validCompactionRegistration(t)

	t.Run("panicking preserver with string is safely recovered and retained", func(t *testing.T) {
		t.Parallel()
		merged := featurebundle.MergedFeatureSurface{
			CompactionPreservers: []compaction.Preserver{
				panickingPreserver{panicVal: "deliberate panic in ID()"},
				charPreserver{id: featurecontinuity.ID},
			},
		}

		res, err := BindFeatureSurface(merged, port, []lipsdk.Registration{reg})
		require.NoError(t, err)
		require.Len(t, res.CompactionPreservers, 2)
		assert.IsType(t, panickingPreserver{}, res.CompactionPreservers[0])
		assert.Equal(t, featurecontinuity.ID, res.CompactionPreservers[1].ID())
	})

	t.Run("panicking preserver with error is safely recovered and retained", func(t *testing.T) {
		t.Parallel()
		merged := featurebundle.MergedFeatureSurface{
			CompactionPreservers: []compaction.Preserver{
				charPreserver{id: "custom-a"},
				panickingPreserver{panicVal: errors.New("id extraction error panic")},
				charPreserver{id: "custom-b"},
			},
		}

		res, err := BindFeatureSurface(merged, port, []lipsdk.Registration{reg})
		require.NoError(t, err)
		require.Len(t, res.CompactionPreservers, 4)
		assert.Equal(t, "custom-a", res.CompactionPreservers[0].ID())
		assert.IsType(t, panickingPreserver{}, res.CompactionPreservers[1])
		assert.Equal(t, "custom-b", res.CompactionPreservers[2].ID())
		assert.Equal(t, featurecontinuity.ID, res.CompactionPreservers[3].ID())
	})

	t.Run("nil preserver interface value is safe and retained", func(t *testing.T) {
		t.Parallel()
		merged := featurebundle.MergedFeatureSurface{
			CompactionPreservers: []compaction.Preserver{
				nil,
				charPreserver{id: featurecontinuity.ID},
				nil,
			},
		}

		res, err := BindFeatureSurface(merged, port, []lipsdk.Registration{reg})
		require.NoError(t, err)
		require.Len(t, res.CompactionPreservers, 3)
		assert.Nil(t, res.CompactionPreservers[0])
		assert.Nil(t, res.CompactionPreservers[1])
		assert.Equal(t, featurecontinuity.ID, res.CompactionPreservers[2].ID())
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

	initialSurface := featurebundle.MergedFeatureSurface{
		CompactionPreservers: []compaction.Preserver{
			charPreserver{id: "preserved-preserver"},
		},
		CompactionObservers: nil,
	}
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
		assert.Equal(t, featurebundle.MergedFeatureSurface{}, res)
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
		assert.Equal(t, featurebundle.MergedFeatureSurface{}, res)
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

func TestBindFeatureSurface_Idempotence(t *testing.T) {
	t.Parallel()
	port := newTestPort(t)
	reg := validCompactionRegistration(t)

	initial := featurebundle.MergedFeatureSurface{
		CompactionPreservers: []compaction.Preserver{
			charPreserver{id: "custom-a"},
			charPreserver{id: featurecontinuity.ID},
			charPreserver{id: "custom-b"},
		},
	}

	res1, err := BindFeatureSurface(initial, port, []lipsdk.Registration{reg})
	require.NoError(t, err)

	res2, err := BindFeatureSurface(res1, port, []lipsdk.Registration{reg})
	require.NoError(t, err)

	res3, err := BindFeatureSurface(res2, port, []lipsdk.Registration{reg})
	require.NoError(t, err)

	require.Len(t, res1.CompactionPreservers, 3)
	require.Len(t, res2.CompactionPreservers, 3)
	require.Len(t, res3.CompactionPreservers, 3)

	for i := 0; i < 3; i++ {
		assert.Equal(t, res1.CompactionPreservers[i].ID(), res2.CompactionPreservers[i].ID())
		assert.Equal(t, res1.CompactionPreservers[i].ID(), res3.CompactionPreservers[i].ID())
	}
}

func TestBindFeatureSurface_DisabledAndNonMatchingRegistrations(t *testing.T) {
	t.Parallel()
	port := newTestPort(t)
	reg := validCompactionRegistration(t)

	initial := featurebundle.MergedFeatureSurface{
		CompactionPreservers: []compaction.Preserver{
			charPreserver{id: "custom-a"},
			charPreserver{id: featurecontinuity.ID},
		},
	}

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
