package product

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunStream_RunSubOverflow_PreservesBufferedThenBridgeProtocolFault(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(910040)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, map[string]func(req *protocol.Frame){
			protocol.MethodAgentSend: func(req *protocol.Frame) {
				res, _ := json.Marshal(protocol.AgentSendResult{RunID: "run-ovf-stream"})
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeResponse,
					ID:            req.ID,
					Method:        req.Method,
					Result:        res,
				}))
				for i := int64(1); i <= 40; i++ {
					seq := i
					proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
						SchemaVersion: protocol.SchemaVersion,
						Type:          protocol.TypeEvent,
						RunID:         "run-ovf-stream",
						Seq:           &seq,
						Kind:          protocol.KindTextDelta,
						Payload:       json.RawMessage(`{"text":"x"}`),
					}))
				}
			},
		})
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	defer func() { _ = bp.Close() }()
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)

	_, err = bp.Call(context.Background(), protocol.MethodAgentSend, json.RawMessage(`{"agentId":"a","prompt":"p"}`))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return bp.runSubClosed("run-ovf-stream")
	}, time.Second, 5*time.Millisecond)

	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-ovf-stream", Generation: bp.Generation()}
	s := NewRunStream(context.Background(), NewBridgeAgentClient(bp), lease, owner, RunStreamOpts{
		MaxPending: maxRunStreamPending,
	})
	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var events []lipapi.Event
	var terminalErr error
	for {
		ev, err := s.Recv(ctx)
		if err != nil {
			terminalErr = err
			break
		}
		events = append(events, ev)
	}

	require.NotEmpty(t, events, "buffered events must be delivered before terminal overflow error")
	require.Error(t, terminalErr)

	var bf *BridgeFault
	require.True(t, errors.As(terminalErr, &bf), "terminal error must be typed BridgeFault, got %T %v", terminalErr, terminalErr)
	assert.Equal(t, CodeBridgeProtocol, bf.Code)
	assert.False(t, errors.Is(terminalErr, ErrBridgeExited), "overflow must not surface as BridgeExited")
	assert.False(t, lipapi.IsRecoverablePreOutput(terminalErr), "overflow is nonrecoverable")

	var sawText bool
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta {
			sawText = true
			break
		}
	}
	assert.True(t, sawText, "at least one buffered text_delta must be preserved before fault")
}
