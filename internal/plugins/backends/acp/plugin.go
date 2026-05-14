package acp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Config configures the ACP HTTP backend connector (JSON-RPC over POST /v1/acp).
type Config struct {
	BaseURL string
	// HTTPClient is optional. When nil, the connector uses infra/httpclient.Standard
	// (explicit Transport timeouts and pooling, matching other bundled backends).
	HTTPClient *http.Client

	// Handshake defaults merged with Call.Extensions (see package doc).
	Handshake HandshakeProfile

	// Cancel configures cancellation RPC shape (defaults: Python-style multi-method list).
	Cancel CancelProfile

	// SessionUpdate configures session/update → canonical event mapping.
	SessionUpdate SessionUpdateMapperOptions

	// ServerRequest handles inbound JSON-RPC from the agent during a prompt (stdio parity).
	ServerRequest ServerRequestHandler

	// History is reserved for transcript-style prompts coordinated with core/B2BUA.
	History HistoryCoordinator

	// Log is optional; when set, the connector may emit debug logs (e.g. best-effort cancel RPC failures).
	Log *slog.Logger
}

func defaultBackendCaps() lipapi.BackendCaps {
	return lipapi.NewBackendCaps(
		lipapi.CapabilityStreaming,
		lipapi.CapabilityVision,
		lipapi.CapabilityDocuments,
		lipapi.CapabilityReasoning,
	)
}

// New returns a runtime backend that invokes an ACP agent via the prompt-turn subset.
func New(cfg Config) execbackend.Backend {
	cli, err := newClient(cfg.BaseURL, cfg.HTTPClient, cfg.Log)
	if err != nil {
		return execbackend.Backend{
			Caps: defaultBackendCaps(),
			ResolveCaps: func(context.Context, lipapi.Call, routing.AttemptCandidate) lipapi.BackendCaps {
				return defaultBackendCaps()
			},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, err
			},
		}
	}
	c := cli
	hist := cfg.History
	if hist == nil {
		hist = noopHistoryCoordinator{}
	}
	mapper := mergeMapperOptions(cfg)
	cancelProf := mergeCancelProfile(cfg)
	return execbackend.Backend{
		Caps: defaultBackendCaps(),
		ResolveCaps: func(context.Context, lipapi.Call, routing.AttemptCandidate) lipapi.BackendCaps {
			return defaultBackendCaps()
		},
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			_ = cand
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", ID, lipapi.ErrNilContext)
			}
			if err := validateACPCall(&call); err != nil {
				return nil, err
			}
			callPtr := &call
			var err error
			callPtr, err = hist.PreparePrompt(ctx, callPtr)
			if err != nil {
				return nil, fmt.Errorf("acp: prepare prompt: %w", err)
			}
			hp := mergeHandshakeProfile(cfg, callPtr)
			if err := runHandshake(ctx, c, hp); err != nil {
				return nil, err
			}
			sid, err := resolveSessionID(ctx, c, callPtr, hp)
			if err != nil {
				return nil, err
			}
			blocks, err := promptBlocksForCall(callPtr)
			if err != nil {
				return nil, err
			}
			msgID := messageIDForCall(callPtr)
			params := buildPromptParams(sid, blocks, msgID)
			rpcID := c.rpcID()
			body, err := c.sessionPrompt(ctx, params, rpcID)
			if err != nil {
				return nil, err
			}
			return newPromptNDJSONStream(ctx, body, c, sid, rpcID, msgID, mapper, cfg.ServerRequest, cancelProf, call.MaxPendingWireEvents), nil
		},
	}
}
