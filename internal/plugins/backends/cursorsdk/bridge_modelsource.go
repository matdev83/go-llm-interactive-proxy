package cursorsdk

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
)

type bridgeCaller interface {
	EnsureReady(ctx context.Context) (BridgeInfo, error)
	Call(ctx context.Context, method string, params json.RawMessage) (*protocol.Frame, error)
}

type bridgeModelListSource struct {
	call   bridgeCaller
	apiKey string
}

func newBridgeModelListSource(bp *bridgeProcess, apiKey string) *bridgeModelListSource {
	return &bridgeModelListSource{call: bp, apiKey: apiKey}
}

func (s *bridgeModelListSource) ListModels(ctx context.Context) ([]protocol.ModelRow, error) {
	if s == nil || s.call == nil {
		return nil, fmt.Errorf("cursorsdk: nil bridge model list source")
	}
	if _, err := s.call.EnsureReady(ctx); err != nil {
		return nil, err
	}
	frame, err := s.call.Call(ctx, protocol.MethodModelsList, mustJSON(protocol.ModelsListParams{APIKey: s.apiKey}))
	if err != nil {
		return nil, err
	}
	if frame.Error != nil {
		return nil, fmt.Errorf("cursorsdk: models/list: %s: %s", frame.Error.Code, frame.Error.Message)
	}
	var out protocol.ModelsListResult
	if err := json.Unmarshal(frame.Result, &out); err != nil {
		return nil, fmt.Errorf("cursorsdk: models/list decode: %w", err)
	}
	rows := make([]protocol.ModelRow, len(out.Models))
	copy(rows, out.Models)
	return rows, nil
}
