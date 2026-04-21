package acp

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// HistoryCoordinator is an optional integration point for transcript-style ACP prompts
// (Python ACPTranscriptSerializer + hash-based divergence). The B2BUA / session store
// should own durable history state; the backend receives hints via Call.Extensions or
// future lipapi.SessionRef fields. Concrete coordinators implement merge/serialize policy.
type HistoryCoordinator interface {
	// PreparePrompt is reserved for future use: transform the canonical call before
	// promptBlocksForCall (e.g. full transcript vs tail-only).
	PreparePrompt(ctx context.Context, call *lipapi.Call) (*lipapi.Call, error)
}

// noopHistoryCoordinator is a placeholder until core continuity wires history state.
type noopHistoryCoordinator struct{}

func (noopHistoryCoordinator) PreparePrompt(_ context.Context, call *lipapi.Call) (*lipapi.Call, error) {
	return call, nil
}
