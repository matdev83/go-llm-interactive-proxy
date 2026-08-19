package runtimebundle

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactioncompose"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func validateCompactionContinuityGeneration(ps *ProcessServices, regs []lipsdk.Registration) error {
	if ps == nil {
		return compactioncompose.ValidateFeaturePrerequisites(regs, false, false, false)
	}
	return compactioncompose.ValidateFeaturePrerequisites(regs, ps.CompactionDetector != nil, ps.BranchCoordinator != nil, ps.BackgroundAux != nil)
}

func bindCompactionContinuity(merged featurebundle.MergedFeatureSurface, ps *ProcessServices, regs []lipsdk.Registration) (featurebundle.MergedFeatureSurface, error) {
	var parent *compactioncompose.CompactionContinuityParentPort
	if ps != nil {
		parent = ps.CompactionParentPort
	}
	return compactioncompose.BindFeatureSurface(merged, parent, regs)
}
