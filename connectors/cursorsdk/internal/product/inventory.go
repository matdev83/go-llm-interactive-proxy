package product

import (
	"context"
	"errors"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type inventoryProvider struct {
	source  ModelListSource
	catalog *Catalog
}

func newInventoryProvider(source ModelListSource, catalog *Catalog) *inventoryProvider {
	return &inventoryProvider{source: source, catalog: catalog}
}

func (p *inventoryProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	if p == nil || p.source == nil {
		return modelinventory.Snapshot{}, &modelinventory.OperationalError{
			Code: modelinventory.ErrorCodeUnavailable,
			Err:  errors.New("cursorsdk: nil model list source"),
		}
	}
	rows, err := p.source.ListModels(ctx)
	if err != nil {
		code := modelinventory.ErrorCodeUnavailable
		if errors.Is(err, context.DeadlineExceeded) {
			code = modelinventory.ErrorCodeTimeout
		} else if errors.Is(err, context.Canceled) {
			code = modelinventory.ErrorCodeCanceled
		}
		return modelinventory.Snapshot{}, &modelinventory.OperationalError{Code: code, Err: err}
	}
	models, entries, err := normalizeModelRows(rows)
	if err != nil {
		return modelinventory.Snapshot{}, &modelinventory.OperationalError{
			Code: modelinventory.ErrorCodeInvalidInventory,
			Err:  err,
		}
	}
	if p.catalog != nil {
		p.catalog.Replace(entries)
	}
	return modelinventory.Snapshot{
		Source:   modelinventory.SourceRemote,
		LoadedAt: time.Now().UTC(),
		Models:   models,
	}, nil
}
