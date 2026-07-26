package runtimebundle

// CandidateFaultInject is a test-only CompileCandidate / CompileGeneration
// failure seam. Production leaves After empty.
type CandidateFaultInject struct {
	// After names the boundary that should fail: "model", "prepare", "activate",
	// or "handler" (ComposeGeneration / request-plane composition).
	After string
	// Hook runs immediately before the injected fault.
	Hook func()
}
