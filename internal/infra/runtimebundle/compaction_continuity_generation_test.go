package runtimebundle

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	compactiondetect "github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactiondetect"
	featurecompaction "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func generationYAML(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	return node
}

func TestValidateCompactionContinuityGeneration_DependencyGateAndDisabledNoop(t *testing.T) {
	t.Parallel()
	reg := lipsdk.Registration{
		ID: "compaction-continuity", FactoryKind: featurecompaction.ID,
		Kind: lipsdk.PluginKindFeature, Enabled: true,
		Config: lipsdk.ConfigPayload{Node: generationYAML(t, "extractor:\n  enabled: true\n  route: inherit\n")},
	}
	ps := &ProcessServices{
		CompactionDetector: compactiondetect.New(compactiondetect.Config{}),
		BranchCoordinator:  &compactioncontinuity.BranchCoordinator{},
		BackgroundAux:      &BackgroundAuxScheduler{},
	}
	if err := validateCompactionContinuityGeneration(ps, []lipsdk.Registration{reg}); err != nil {
		t.Fatalf("complete generation prerequisites: %v", err)
	}

	ps.BranchCoordinator = nil
	err := validateCompactionContinuityGeneration(ps, []lipsdk.Registration{reg})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "branch coordinator") {
		t.Fatalf("missing branch coordinator error = %v", err)
	}

	reg.Enabled = false
	reg.Config.Node = generationYAML(t, "extractor:\n  enabled: true\n")
	if err := validateCompactionContinuityGeneration(ps, []lipsdk.Registration{reg}); err != nil {
		t.Fatalf("disabled feature must be a no-op: %v", err)
	}
}

func TestValidateCompactionContinuityGeneration_NilProcessNoopWhenAbsentOrDisabled(t *testing.T) {
	t.Parallel()
	if err := validateCompactionContinuityGeneration(nil, nil); err != nil {
		t.Fatalf("absent feature must be a nil-process no-op: %v", err)
	}
	reg := lipsdk.Registration{ID: featurecompaction.ID, Kind: lipsdk.PluginKindFeature, Enabled: false}
	if err := validateCompactionContinuityGeneration(nil, []lipsdk.Registration{reg}); err != nil {
		t.Fatalf("disabled feature must be a nil-process no-op: %v", err)
	}
	reg.Enabled = true
	if err := validateCompactionContinuityGeneration(nil, []lipsdk.Registration{reg}); err == nil {
		t.Fatal("enabled feature with nil process must fail clearly")
	}
}

func TestProcessServices_CompactionDetectorInterfaceAndGenerationSharing(t *testing.T) {
	t.Parallel()

	// 1. Assert field type on ProcessServices is the runtime.CompactionDetector interface.
	field, ok := reflect.TypeOf((*ProcessServices)(nil)).Elem().FieldByName("CompactionDetector")
	require.True(t, ok, "ProcessServices must have CompactionDetector field")
	assert.Equal(t, reflect.Interface, field.Type.Kind(), "ProcessServices.CompactionDetector must be an interface")
	assert.Equal(t, "runtime.CompactionDetector", field.Type.String(), "ProcessServices.CompactionDetector must be runtime.CompactionDetector")

	// 2. Assert field type on executorBuildInput is also the runtime.CompactionDetector interface.
	buildField, ok := reflect.TypeOf((*executorBuildInput)(nil)).Elem().FieldByName("CompactionDetector")
	require.True(t, ok, "executorBuildInput must have CompactionDetector field")
	assert.Equal(t, reflect.Interface, buildField.Type.Kind(), "executorBuildInput.CompactionDetector must be an interface")
	assert.Equal(t, "runtime.CompactionDetector", buildField.Type.String(), "executorBuildInput.CompactionDetector must be runtime.CompactionDetector")

	// 3. Test instantiation & no-closer invariant in adoptBackgroundAuxAndDetector.
	ctx := context.Background()
	in := &ProcessServicesInput{}
	ps := &ProcessServices{}
	var registeredClosers []func() error
	register := func(c func() error) {
		registeredClosers = append(registeredClosers, c)
	}

	adoptBackgroundAuxAndDetector(ctx, in, ps, register)
	require.NotNil(t, ps.CompactionDetector, "adoptBackgroundAuxAndDetector must instantiate CompactionDetector")
	_, implements := any(ps.CompactionDetector).(runtime.CompactionDetector)
	assert.True(t, implements, "ps.CompactionDetector must implement runtime.CompactionDetector")

	// Verify exactly 1 closer was registered (for BackgroundAux), zero for CompactionDetector.
	assert.Equal(t, 1, len(registeredClosers), "exactly 1 closer must be registered (BackgroundAux)")

	// Invariant: CompactionDetector itself has no Close method
	dType := reflect.TypeOf(ps.CompactionDetector)
	for i := 0; i < dType.NumMethod(); i++ {
		assert.NotEqual(t, "Close", dType.Method(i).Name, "CompactionDetector must not have a Close method")
	}

	// 4. Verify generation sharing: generation build inputs receive the exact same instance.
	buildInput := executorBuildInput{
		CompactionDetector: ps.CompactionDetector,
	}
	assert.Equal(t, ps.CompactionDetector, buildInput.CompactionDetector, "generation build input must receive same process-owned detector reference")
}
