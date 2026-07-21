package runtimebundle

// CandidateFaultInject is a test-only CompileCandidate failure seam.
// Production leaves After empty.
type CandidateFaultInject struct {
	// After names the boundary that should fail: "model", "prepare", or "activate".
	After string
	// Hook runs immediately before the injected fault.
	Hook func()
}
