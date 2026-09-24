package billing

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// BoundStatementImporter adapts one authenticated trusted scope to the frozen
// public economics.StatementImporter seam. The host constructs it from the
// authenticated import context; the public signature cannot choose or widen
// the scope, and no SQL, provider or persistence type crosses this boundary.
type BoundStatementImporter struct {
	service *StatementImportService
	scope   TrustedStatementScope
}

var _ economics.StatementImporter = (*BoundStatementImporter)(nil)

// NewBoundStatementImporter constructs a scope-bound adapter. It rejects a
// missing service or an incomplete scope before any statement is trusted.
func NewBoundStatementImporter(service *StatementImportService, scope TrustedStatementScope) (*BoundStatementImporter, error) {
	if service == nil {
		return nil, fmt.Errorf("%w: statement import service is required", ErrStatementImportLedgerUnavailable)
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	return &BoundStatementImporter{service: service, scope: scope.Clone()}, nil
}

// Import implements economics.StatementImporter with the constructor-bound
// authenticated scope.
func (b *BoundStatementImporter) Import(ctx context.Context, in economics.StatementBatch) (economics.ImportResult, error) {
	if b == nil || b.service == nil {
		return economics.ImportResult{}, fmt.Errorf("%w: bound statement importer is not constructed", ErrStatementImportLedgerUnavailable)
	}
	return b.service.Import(ctx, b.scope, in)
}
