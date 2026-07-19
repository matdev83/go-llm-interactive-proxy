package cursorsdk

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
)

type AgentBridge interface {
	Generation() int64
	EnsureReady(ctx context.Context) (BridgeInfo, error)
	CreateAgent(ctx context.Context, params protocol.AgentCreateParams) (string, error)
	SendAgent(ctx context.Context, agentID, prompt string) (string, error)
	DisposeAgent(ctx context.Context, agentID string) error
	SubscribeRun(runID string) (<-chan *protocol.Frame, func(), func() error)
	CancelRun(ctx context.Context, runID string) error
}

type RunBridge interface {
	SubscribeRun(runID string) (<-chan *protocol.Frame, func(), func() error)
	CancelRun(ctx context.Context, runID string) error
}

type bridgeAgentClient struct {
	bp *bridgeProcess
}

func NewBridgeAgentClient(bp *bridgeProcess) AgentBridge {
	return &bridgeAgentClient{bp: bp}
}

func (c *bridgeAgentClient) Generation() int64 { return c.bp.Generation() }

func (c *bridgeAgentClient) EnsureReady(ctx context.Context) (BridgeInfo, error) {
	return c.bp.EnsureReady(ctx)
}

func (c *bridgeAgentClient) CreateAgent(ctx context.Context, params protocol.AgentCreateParams) (string, error) {
	params.EnableAgentRetries = false
	frame, err := c.bp.Call(ctx, protocol.MethodAgentCreate, mustJSON(params))
	if err != nil {
		return "", err
	}
	if frame.Error != nil {
		return "", fmt.Errorf("cursorsdk: agent/create: %s: %s", frame.Error.Code, frame.Error.Message)
	}
	var out protocol.AgentCreateResult
	if err := json.Unmarshal(frame.Result, &out); err != nil {
		return "", fmt.Errorf("cursorsdk: agent/create decode: %w", err)
	}
	if out.AgentID == "" {
		return "", fmt.Errorf("cursorsdk: agent/create missing agentId")
	}
	return out.AgentID, nil
}

func (c *bridgeAgentClient) SendAgent(ctx context.Context, agentID, prompt string) (string, error) {
	frame, err := c.bp.Call(ctx, protocol.MethodAgentSend, mustJSON(protocol.AgentSendParams{
		AgentID: agentID,
		Prompt:  prompt,
	}))
	if err != nil {
		return "", err
	}
	if frame.Error != nil {
		return "", fmt.Errorf("cursorsdk: agent/send: %s: %s", frame.Error.Code, frame.Error.Message)
	}
	var out protocol.AgentSendResult
	if err := json.Unmarshal(frame.Result, &out); err != nil {
		return "", fmt.Errorf("cursorsdk: agent/send decode: %w", err)
	}
	if out.RunID == "" {
		return "", fmt.Errorf("cursorsdk: agent/send missing runId")
	}
	return out.RunID, nil
}

func (c *bridgeAgentClient) DisposeAgent(ctx context.Context, agentID string) error {
	frame, err := c.bp.Call(ctx, protocol.MethodAgentDispose, mustJSON(protocol.AgentDisposeParams{AgentID: agentID}))
	if err != nil {
		return err
	}
	if frame.Error != nil {
		return fmt.Errorf("cursorsdk: agent/dispose: %s: %s", frame.Error.Code, frame.Error.Message)
	}
	return nil
}

func (c *bridgeAgentClient) SubscribeRun(runID string) (<-chan *protocol.Frame, func(), func() error) {
	return c.bp.SubscribeRun(runID)
}

func (c *bridgeAgentClient) CancelRun(ctx context.Context, runID string) error {
	frame, err := c.bp.Call(ctx, protocol.MethodRunCancel, mustJSON(protocol.RunCancelParams{RunID: runID}))
	if err != nil {
		return err
	}
	if frame.Error != nil {
		return fmt.Errorf("cursorsdk: run/cancel: %s: %s", frame.Error.Code, frame.Error.Message)
	}
	return nil
}
