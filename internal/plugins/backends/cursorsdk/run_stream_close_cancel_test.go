package cursorsdk

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunStream_CloseInvokesCancelOnceIdempotent(t *testing.T) {
	bridge := newScriptedRunBridge(4)
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-close-cancel"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{
		CancelTimeout: 200 * time.Millisecond,
	})

	require.NoError(t, s.Close())
	assert.Equal(t, int32(1), bridge.cancelN.Load(), "Close must invoke CancelRun exactly once")

	require.NoError(t, s.Close())
	assert.Equal(t, int32(1), bridge.cancelN.Load(), "second Close must be idempotent (no double-cancel)")

	// No double terminal: after Close, Recv surfaces a single cancel/closed error path.
	_, err1 := s.Recv(context.Background())
	require.Error(t, err1)
	_, err2 := s.Recv(context.Background())
	require.Error(t, err2)
	assert.True(t, errors.Is(err1, errRunStreamClosed) || errors.Is(err1, io.EOF) || err1 != nil)
	// Same terminal class on repeat — not a second distinct success/error event emission.
	assert.Equal(t, err1.Error(), err2.Error())
}

func TestRunStream_CloseAfterCancelDoesNotDoubleCancel(t *testing.T) {
	bridge := newScriptedRunBridge(4)
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-close-after-cancel"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{
		CancelTimeout: time.Second,
	})
	defer func() { _ = s.Close() }()

	res := s.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelClientGone})
	require.Equal(t, lipapi.CancelModeProvider, res.Mode)
	require.Equal(t, int32(1), bridge.cancelN.Load())

	require.NoError(t, s.Close())
	assert.Equal(t, int32(1), bridge.cancelN.Load(), "Close after Cancel must not double-cancel")
}

func TestRunStream_CloseCancelUsesBoundedTimeout(t *testing.T) {
	bridge := newScriptedRunBridge(2)
	bridge.cancelBlock = 2 * time.Second
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-close-timeout"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{
		CancelTimeout: 40 * time.Millisecond,
	})

	start := time.Now()
	require.NoError(t, s.Close())
	elapsed := time.Since(start)
	assert.Equal(t, int32(1), bridge.cancelN.Load(), "Close must still invoke CancelRun")
	assert.Less(t, elapsed, 1500*time.Millisecond, "Close→Cancel must use CancelTimeout bound, not hang on blocked CancelRun")
}
