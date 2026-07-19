package cursorsdk

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
)

type ModelListSource interface {
	ListModels(ctx context.Context) ([]protocol.ModelRow, error)
}

type StaticModelListSource struct {
	Rows []protocol.ModelRow
	Err  error
}

func (s StaticModelListSource) ListModels(context.Context) ([]protocol.ModelRow, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	out := make([]protocol.ModelRow, len(s.Rows))
	copy(out, s.Rows)
	return out, nil
}
