package runtimebundle

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactiondetect"
	featurecompaction "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
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
