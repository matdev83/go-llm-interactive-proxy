package hooks

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ToolDecision is the outcome of a tool reactor for one tool event.
type ToolDecision int

const (
	ToolPass ToolDecision = iota
	ToolRewrite
	ToolSwallow
	ToolReplace
)

// ToolMeta carries stream and session context for tool reactors.
type ToolMeta struct {
	TraceID    string
	ALegID     string
	BLegID     string
	AttemptSeq int
}

// ToolReactor observes canonical tool lifecycle events and may rewrite output.
type ToolReactor interface {
	ID() string
	Order() int
	HandleToolEvent(ctx context.Context, te lipapi.ToolEvent, meta ToolMeta) (ToolDecision, lipapi.ToolEvent, error)
}

// ToolReactorErrorPolicy selects how the hook bus treats a non-nil error from HandleToolEvent.
type ToolReactorErrorPolicy int

const (
	// ToolReactorErrorsFailOpen preserves the current event and continues the chain (default).
	ToolReactorErrorsFailOpen ToolReactorErrorPolicy = iota
	// ToolReactorErrorsFailClosed stops the chain and surfaces the error to the stream runner.
	ToolReactorErrorsFailClosed
	// ToolReactorErrorsSwallowEvent drops the current tool event (same effect as ToolSwallow).
	ToolReactorErrorsSwallowEvent
)
