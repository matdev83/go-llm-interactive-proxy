package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/require"
)

type panicCancelStream struct {
	lipapi.EventStream
}

func (p *panicCancelStream) Cancel(_ context.Context, _ lipapi.CancelCause) lipapi.CancelResult {
	panic("inner stream cancel exploded")
}

func TestWireLifecycleEventStream_Cancel_CleanupRunsOnPanic(t *testing.T) {
	t.Parallel()

	var cleanedUp atomic.Bool
	wles := &wireLifecycleEventStream{
		EventStream: &panicCancelStream{EventStream: lipapi.NewFixedEventStream(nil)},
		cleanup: func(err error) {
			cleanedUp.Store(true)
		},
	}

	require.PanicsWithValue(t, "inner stream cancel exploded", func() {
		wles.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelExplicit})
	})

	require.True(t, cleanedUp.Load(), "cleanup callback must run even if inner ManagedEventStream.Cancel panics")
}
