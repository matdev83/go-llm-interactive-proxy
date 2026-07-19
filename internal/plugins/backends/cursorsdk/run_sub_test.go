package cursorsdk

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunSub_DeliverBufferOverflowFailsAndCloses(t *testing.T) {
	sub := newRunSub(1)
	for i := int64(1); i <= 32; i++ {
		require.NoError(t, sub.deliver(eventFrame("run-buf", i, protocol.KindTextDelta, `{"text":"x"}`)))
	}
	err := sub.deliver(eventFrame("run-buf", 33, protocol.KindTextDelta, `{"text":"y"}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, errRunSubOverflow)
	n := 0
	for range sub.ch {
		n++
	}
	assert.Equal(t, 32, n)
	err2 := sub.deliver(eventFrame("run-buf", 34, protocol.KindTextDelta, `{"text":"z"}`))
	require.Error(t, err2)
}

func TestRunSub_DeliverTerminalClosesAndRejectsLate(t *testing.T) {
	sub := newRunSub(1)
	require.NoError(t, sub.deliver(eventFrame("run-t", 1, protocol.KindFinished, `{"status":"finished"}`)))
	got := <-sub.ch
	require.Equal(t, protocol.KindFinished, got.Kind)
	_, ok := <-sub.ch
	require.False(t, ok)
	err := sub.deliver(eventFrame("run-t", 2, protocol.KindTextDelta, `{"text":"late"}`))
	require.Error(t, err)
	assert.True(t, sub.seq.Terminated("run-t"))
	assert.ErrorIs(t, err, errRunSubClosed)
}
