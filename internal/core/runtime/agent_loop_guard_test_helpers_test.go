package runtime

import (
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopgate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
)

func newLoopGuardForTest(verifier stopguard.Verifier) *LoopGuard {
	gate := stopgate.New(stopgate.Ports{Verifier: verifier, Now: time.Now}, stopgate.Config{
		Enabled:                  true,
		ExplicitCompletionPolicy: stopguard.PolicyTrust,
		MaxSemanticContinuations: 3,
		NoProgressLimit:          2,
	})
	return NewLoopGuard(gate)
}

func newLoopGuardFactoryForTest(verifier stopguard.Verifier) *LoopGuardFactory {
	return NewLoopGuardFactory(stopgate.Ports{Verifier: verifier, Now: time.Now}, stopgate.Config{
		Enabled:                  true,
		ExplicitCompletionPolicy: stopguard.PolicyTrust,
		MaxSemanticContinuations: 3,
		NoProgressLimit:          2,
	})
}
