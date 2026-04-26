package config

import "strings"

// EffectiveContinuityStore returns the continuity backing name after applying the same
// rules as [Validate] (in_memory forces memory; empty store defaults to memory).
func EffectiveContinuityStore(c ContinuityConfig) string {
	store := strings.ToLower(strings.TrimSpace(c.Store))
	if c.InMemory {
		return "memory"
	}
	if store == "" {
		return "memory"
	}
	return store
}
