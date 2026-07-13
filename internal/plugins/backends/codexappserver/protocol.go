package codexappserver

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// codexProtocol adapts the Codex app-server protocol to acp.SubprocessProtocol,
// letting the shared subprocessBackend orchestrator drive the Codex backend
// alongside the ACP-session connectors. It owns the Codex-specific pieces:
// model resolution (openai/ prefix stripping + auto handling), the
// "codex app-server --stdio" spawn command, the initialize/initialized/thread/start
// handshake, the turn/start prompt send, and the codexStream/codexManagedStream
// construction. The lifecycle skeleton (workspace, pool, history divergence,
// ensure-process) lives in the orchestrator.
type codexProtocol struct {
	spec *codexSpec
}

func (p *codexProtocol) Label() string { return "codex app-server" }

func (p *codexProtocol) ValidateCall(call *lipapi.Call) error { return validateCall(call) }

// ResolveModel applies the Codex model resolution: start from the configured
// default, override with the route model (stripping the openai/ vendor prefix)
// when present, and collapse empty/auto to "auto".
func (p *codexProtocol) ResolveModel(call *lipapi.Call) string {
	model := p.spec.cfg.Model
	if m := acp.CallRouteModel(call, "codex.model"); m != "" {
		model = stripOpenAIModelPrefix(m)
	}
	if isAutoModel(model) {
		model = autoModelSentinel
	}
	return model
}

func (p *codexProtocol) ResolveProcessConfig(call *lipapi.Call) string {
	if call != nil && call.Options.Verbosity != "" {
		return string(call.Options.Verbosity)
	}
	return string(p.spec.cfg.DefaultVerbosity)
}

// BuildSpawnCommand builds the "codex app-server --stdio" launch command from
// the executable resolved once at construction (spec.exe). The cwd is the
// resolved workspace; env is nil (the subprocess inherits the parent
// environment). Re-resolving here would fail on CI runners that lack the real
// codex binary, so the constructor owns resolution (test mode uses a placeholder).
func (p *codexProtocol) BuildSpawnCommand(_, workspace, processConfig string) ([]string, string, []string, error) {
	cmd := buildCodexCommandWithVerbosity(p.spec.exe, p.spec.cfg.ConfigOverrides, lipapi.VerbosityLevel(processConfig), p.spec.cfg.ExtraArgs)
	return cmd, workspace, nil, nil
}

// Handshake runs the Codex initialize/initialized/thread/start exchange and
// returns the thread id. model and workspace are threaded into thread/start.
func (p *codexProtocol) Handshake(ctx context.Context, transport acp.Transport, model, workspace string) (string, error) {
	cli := newCodexClient(transport)
	threadID, err := runCodexHandshake(ctx, cli, transport, workspace, model)
	if err != nil {
		return "", fmt.Errorf("handshake: %w", err)
	}
	return threadID, nil
}

func (p *codexProtocol) BindSession(transport acp.Transport, sessionID string) acp.SubprocessProtocolSession {
	return &codexProtocolSession{
		cli:      newCodexClient(transport),
		threadID: sessionID,
	}
}

// codexProtocolSession is the per-request Codex handle. It carries the thread
// id from BindSession and the rpc id from SendPrompt into BuildStream (the
// stream needs the rpc id to match the turn/start terminal response).
type codexProtocolSession struct {
	cli      *codexClient
	threadID string
}

func (s *codexProtocolSession) SendPrompt(ctx context.Context, model, userMessage string, call *lipapi.Call) (io.ReadCloser, int64, error) {
	if strings.TrimSpace(userMessage) == "" {
		return nil, 0, fmt.Errorf("no user message found")
	}
	turnParams := map[string]any{
		"threadId": s.threadID,
		"input":    []map[string]any{{"type": "text", "text": userMessage}},
	}
	if !isAutoModel(model) {
		turnParams["model"] = model
	}
	if effort := extractReasoningEffort(call); effort != "" {
		turnParams["effort"] = strings.ToLower(strings.TrimSpace(effort))
	}

	turnRPCID := s.cli.rpcID()
	turnBody, err := buildTurnStartRequest(turnParams, turnRPCID)
	if err != nil {
		return nil, 0, fmt.Errorf("turn/start marshal: %w", err)
	}
	body, err := s.cli.t.CallPromptStream(ctx, turnBody)
	if err != nil {
		return nil, 0, fmt.Errorf("turn/start: %w", err)
	}
	return body, turnRPCID, nil
}

func (s *codexProtocolSession) BuildStream(parent context.Context, body io.ReadCloser, rpcID int64, pool *acp.RuntimePool, key acp.RuntimeKey, maxPending int) (lipapi.ManagedEventStream, error) {
	stream := newCodexStream(parent, body, s.cli, rpcID, &codexServerRequestHandler{}, maxPending)
	return &codexManagedStream{inner: stream, pool: pool, key: key}, nil
}
