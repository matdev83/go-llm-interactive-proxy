package runtimebundle

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactioncompose"
)

func bindSharedMutableProcessServices(ctx context.Context, ps *ProcessServices, shared *sharedMutableRuntime) error {
	ps.sharedMutable = shared
	ps.ALegLifecycle = shared.ALegLifecycle
	ps.ExtensionState = shared.ExtensionState
	ps.AffinityStore = &processAffinityHandle{reg: shared.affinity}
	ps.CandidateHealth = shared.underlyingHealth
	var err error
	ps.BranchCoordinator, err = compactioncontinuity.NewBranchCoordinator(ctx, compactioncontinuity.Config{Store: ps.ExtensionState})
	if err != nil {
		return err
	}
	ps.CompactionParentPort, err = compactioncompose.NewCompactionContinuityParentPort(ps.BranchCoordinator)
	return err
}
