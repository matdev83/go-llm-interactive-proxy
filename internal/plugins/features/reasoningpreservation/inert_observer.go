package reasoningpreservation

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
)

// inertStreamObserver is a package-private no-op used when ResolveMatch is not eligible.
// It performs no event parsing, buffering, store I/O, or telemetry.
type inertStreamObserver struct{}

func (inertStreamObserver) Observe(context.Context, lipapi.Event) error { return nil }

func (inertStreamObserver) Finish(context.Context, response.StreamOutcome) error { return nil }
