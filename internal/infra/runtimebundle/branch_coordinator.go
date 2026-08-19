package runtimebundle

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactioncontinuity"
	lipstate "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
)

// newProcessBranchCoordinator binds the coordinator to the process-owned
// ExtensionState. Generation snapshots retain only non-owning access through
// ProcessServices; reload therefore cannot split branch state by generation.
func newProcessBranchCoordinator(store lipstate.Store) (*compactioncontinuity.BranchCoordinator, error) {
	return compactioncontinuity.NewBranchCoordinator(compactioncontinuity.Config{Store: store})
}

func bindSharedMutableProcessServices(ps *ProcessServices, shared *sharedMutableRuntime) error {
	ps.sharedMutable = shared
	ps.ALegLifecycle = shared.ALegLifecycle
	ps.ExtensionState = shared.ExtensionState
	ps.AffinityStore = &processAffinityHandle{reg: shared.affinity}
	ps.CandidateHealth = shared.underlyingHealth
	var err error
	ps.BranchCoordinator, err = newProcessBranchCoordinator(ps.ExtensionState)
	return err
}
