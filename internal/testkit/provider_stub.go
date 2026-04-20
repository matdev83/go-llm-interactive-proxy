package testkit

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ProviderStub is a minimal no-op backend stand-in for conformance and wiring tests.
type ProviderStub struct {
	ID string
}

// Invoke returns an empty stream placeholder until the canonical stream engine exists.
func (ProviderStub) Invoke(context.Context, lipapi.Call) ([]lipapi.Event, error) {
	return nil, nil
}
