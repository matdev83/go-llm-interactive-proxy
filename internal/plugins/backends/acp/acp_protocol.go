package acp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// acpProtocol adapts a SubprocessConnectorSpec (the per-connector seam
// implemented by cursorcliacp, geminicliacp, agycliacp) to the
// SubprocessProtocol interface consumed by the shared subprocessBackend
// orchestrator. It owns the ACP-session-protocol logic that is shared across
// those connectors (session/prompt send, promptStream construction,
// poolManagedStream wrapping with tool-summary flushing), parameterized by the
// vendor-specific pieces on the spec (BuildCommand, HandshakeProfile,
// CancelProfile, ServerRequestHandler, ResolveModel).
type acpProtocol struct {
	spec     SubprocessConnectorSpec
	log      *slog.Logger
	toolSink *toolSummarySink
}

// NewACPProtocol wraps a SubprocessConnectorSpec as a SubprocessProtocol so
// the shared subprocess orchestrator can drive it. log may be nil (defaults to
// slog.Default). The returned protocol owns a toolSummarySink used to flush
// incomplete tool summaries at stream EOF.
func NewACPProtocol(spec SubprocessConnectorSpec, log *slog.Logger) SubprocessProtocol {
	if log == nil {
		log = slog.Default()
	}
	return &acpProtocol{
		spec:     spec,
		log:      log,
		toolSink: &toolSummarySink{tools: make(map[string]*toolAccum), now: time.Now},
	}
}

func (p *acpProtocol) Label() string { return "acp subprocess" }

func (p *acpProtocol) ValidateCall(call *lipapi.Call) error { return validateACPCall(call) }

func (p *acpProtocol) ResolveModel(call *lipapi.Call) string {
	return p.spec.ResolveModel(CallRouteModel(call, "acp.model"))
}

func (p *acpProtocol) ResolveProcessConfig(_ *lipapi.Call) string { return "" }

func (p *acpProtocol) BuildSpawnCommand(model, workspace, _ string) ([]string, string, []string, error) {
	return p.spec.BuildCommand(model, workspace)
}

func (p *acpProtocol) Handshake(ctx context.Context, transport Transport, _, _ string) (string, error) {
	cli := newClientFromTransport(transport, p.log)
	hp := p.spec.HandshakeProfile()
	if err := runHandshake(ctx, cli, hp); err != nil {
		return "", fmt.Errorf("handshake: %w", err)
	}
	sid, err := cli.sessionNew(ctx, hp)
	if err != nil {
		return "", fmt.Errorf("session/new: %w", err)
	}
	return sid, nil
}

func (p *acpProtocol) BindSession(transport Transport, sessionID string) SubprocessProtocolSession {
	return &acpProtocolSession{
		proto:     p,
		cli:       newClientFromTransport(transport, p.log),
		sessionID: sessionID,
	}
}

// acpProtocolSession is the per-request ACP-session handle. It carries the
// message id from SendPrompt to BuildStream (the stream needs it for
// cancel-session RPCs), which is safe because the orchestrator creates a fresh
// session per Open call.
type acpProtocolSession struct {
	proto     *acpProtocol
	cli       *client
	sessionID string
	msgID     string
}

func (s *acpProtocolSession) SendPrompt(ctx context.Context, model, userMessage string, call *lipapi.Call) (io.ReadCloser, int64, error) {
	callPtr := prepareTranscriptCall(call, userMessage)
	blocks, err := promptBlocksForCall(callPtr)
	if err != nil {
		return nil, 0, fmt.Errorf("session/prompt: %w", err)
	}
	s.msgID = messageIDForCall(callPtr)
	params := buildPromptParams(s.sessionID, blocks, s.msgID)
	rpcID := s.cli.rpcID()
	body, err := s.cli.sessionPrompt(ctx, params, rpcID)
	if err != nil {
		return nil, 0, fmt.Errorf("session/prompt: %w", err)
	}
	return body, rpcID, nil
}

func (s *acpProtocolSession) BuildStream(parent context.Context, body io.ReadCloser, rpcID int64, pool *RuntimePool, key RuntimeKey, maxPending int) (lipapi.ManagedEventStream, error) {
	mapper := SessionUpdateMapperOptions{ToolSink: s.proto.toolSink}
	cancelProf := s.proto.spec.CancelProfile()
	srv := s.proto.spec.ServerRequestHandler()
	stream := newPromptNDJSONStream(parent, body, s.cli, s.sessionID, rpcID, s.msgID, mapper, srv, cancelProf, maxPending)
	return &poolManagedStream{inner: stream, pool: pool, key: key, toolSink: s.proto.toolSink}, nil
}
