package runtimebundle

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/ingressdefense"
)

// configureSelfDefenseProcessService owns the single process-lifetime ingress
// self-defense adaptive state.
//
// The state is built from the effective startup-fixed max_entries and state_ttl
// (requirement 7.4: both are restart-required because they size process-owned
// resources shared by every generation). Request policy stays generation-scoped,
// so the state is constructed even when self-defense is currently disabled:
// enabled is reloadable, and a process that starts disabled must still own the
// bounded table a later enabling generation needs (requirement 1.2, design
// validation concern 7). While disabled no request consults it, so the ownership
// stays lightweight: one empty bounded table.
//
// The state owns no goroutine, file, database or network resource, so no closer
// is registered: expiry is lazy on lookup and mutation, and disposal is exactly
// the drop of the reference when the owning ProcessServices is closed. Every
// generation receives only a borrowed pointer and can neither close nor resize
// it (requirement 7.6).
func configureSelfDefenseProcessService(ps *ProcessServices, cfg *config.Config) error {
	if ps == nil || cfg == nil {
		return fmt.Errorf("runtimebundle: ingress self-defense process service requires config")
	}
	compiled, err := config.CompileSelfDefense(cfg.Access.SelfDefense)
	if err != nil {
		return err
	}
	state, err := ingressdefense.NewState(compiled.StateLimits())
	if err != nil {
		return fmt.Errorf("runtimebundle: ingress self-defense state: %w", err)
	}
	ps.IngressDefense = state
	return nil
}
