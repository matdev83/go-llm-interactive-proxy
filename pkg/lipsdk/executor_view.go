package lipsdk

import (
	"context"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ExecutorView is the minimal HTTP-facing surface bundled frontends use from the runtime.
// The standard *runtime.Executor implements this interface.
type ExecutorView interface {
	Execute(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error)
	// WallClock returns the optional wall clock callback used for response metadata; nil means unset.
	WallClock() func() time.Time
}
