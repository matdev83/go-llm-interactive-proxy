package product_test

import (
	"context"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
)

type scriptedModelListSource struct {
	mu   sync.Mutex
	rows []protocol.ModelRow
	err  error
}

func (s *scriptedModelListSource) ListModels(context.Context) ([]protocol.ModelRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	out := make([]protocol.ModelRow, len(s.rows))
	copy(out, s.rows)
	return out, nil
}

func (s *scriptedModelListSource) set(rows []protocol.ModelRow, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append([]protocol.ModelRow(nil), rows...)
	s.err = err
}
