package continuation

import (
	"context"

	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

// MemoryStore is a compatibility name for the SDK-owned continuation store.
// Core uses the public protocol-neutral implementation and adds no mutable
// continuation state of its own.
type MemoryStore = lipcont.MemoryStore

func NewMemoryStore() *MemoryStore { return lipcont.NewMemoryStore() }

func NewMemoryStoreWithLimits(limits lipcont.StorageLimits) *MemoryStore {
	return lipcont.NewMemoryStoreWithLimits(limits)
}

// StreamRecorder is a compatibility name for the SDK-owned recorder.
type StreamRecorder = lipcont.StreamRecorder

func NewStreamRecorder(recorder lipcont.Recorder, record lipcont.ContinuationRecord, cleanup func()) *StreamRecorder {
	return lipcont.NewStreamRecorder(recorder, record, cleanup)
}

// NewResponseID delegates ID generation to the SDK authority.
func NewResponseID(ctx context.Context) (lipcont.ResponseID, error) {
	return lipcont.NewResponseID(ctx)
}
