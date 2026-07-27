package product

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPendingEventQueue_BoundedPushPop(t *testing.T) {
	t.Parallel()
	q := NewPendingEventQueue(2)
	require.NoError(t, q.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "a"}))
	require.NoError(t, q.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "b"}))
	err := q.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "c"})
	require.ErrorIs(t, err, ErrPendingQueueFull)
	assert.Equal(t, 2, q.Len())

	ev, ok := q.PopFront()
	require.True(t, ok)
	assert.Equal(t, "a", ev.Delta)
	require.NoError(t, q.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "c"}))
	assert.Equal(t, 2, q.Len())
}

func TestPendingEventQueue_UnboundedAndEmpty(t *testing.T) {
	t.Parallel()
	q := NewPendingEventQueue(0)
	require.NoError(t, q.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}))
	_, ok := q.PopFront()
	require.True(t, ok)
	_, ok = q.PopFront()
	require.False(t, ok)
	assert.Equal(t, 0, q.Len())
}
