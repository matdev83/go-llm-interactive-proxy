package billing

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// ShadowEvidenceSink is the capture-only persistence port for V2 shadow
// observations. Implementations persist immutable observations with stable
// source identity and idempotent replay through existing V2 observation
// storage. They must not create usage call/leg rows, claim state,
// provider-cost work, exposures, journals, unit operations, balance
// mutations, adjustments, or any payable intent. The method shape matches the
// metering journal writer, which satisfies it without a new store; it can
// never be satisfied by, or accidentally route through, the ordinary terminal
// handoff.
type ShadowEvidenceSink interface {
	AppendObservations(context.Context, []metering.Observation) error
}
