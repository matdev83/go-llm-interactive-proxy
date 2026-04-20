package hooks

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// PartMeta carries request-scoped identifiers for part hooks (no globals).
type PartMeta struct {
	TraceID    string
	ALegID     string
	BLegID     string
	AttemptSeq int
}

// RequestPartHook mutates canonical request parts before backend translation.
type RequestPartHook interface {
	ID() string
	Order() int
	FailureMode() FailureMode
	HandleRequestParts(ctx context.Context, call *lipapi.Call, meta PartMeta) error
}

// ResponsePartHook mutates a single canonical stream event before frontend encoding.
type ResponsePartHook interface {
	ID() string
	Order() int
	FailureMode() FailureMode
	HandleEvent(ctx context.Context, ev *lipapi.Event, meta PartMeta) error
}
