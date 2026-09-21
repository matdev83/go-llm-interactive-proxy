package runtimebundle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	billingadmin "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// operatorReportsForMount projects the optional 16.2A operator readers from
// the composed billing reports port. Stores without a reader capability
// yield nil members so their routes report disabled instead of failing.
// Stock composition never opts into the normalized statement import: the
// returned StatementImport is always nil and only explicit host code may
// set it on the composed operations input.
//
// Finding 4B: the economic-detail reader is additionally composed with the
// metering journal's exact observation reader when the mounted metering
// querier exposes it. This is opt-in and additive: an absent journal yields an
// explicit unavailable statement reader, and a present journal resolves the
// exact statement-origin S observations referenced by in-scope valuations.
func operatorReportsForMount(reports billing.ReportingStore, meteringQuerier metering.Querier) billingadmin.OperatorReports {
	var out billingadmin.OperatorReports
	if reports == nil {
		return out
	}
	if reader, ok := reports.(billing.EconomicDetailReader); ok {
		out.EconomicDetail = billing.NewStatementEvidenceEconomicDetailReader(reader, statementObservationSource(meteringQuerier, reports))
	}
	if reader, ok := reports.(economics.DiscrepancyReader); ok {
		out.Discrepancies = reader
	}
	if reader, ok := reports.(economics.StatementLineReader); ok {
		out.StatementLines = reader
	}
	if reader, ok := reports.(economics.AdjustmentReader); ok {
		out.Adjustments = reader
	}
	if reader, ok := reports.(billing.EconomicHealthReader); ok {
		out.Health = reader
	}
	if source, ok := meteringQuerier.(billingstore.AccountWindowSource); ok && source != nil {
		reader := billingstore.AllowanceReader{Source: source}
		if authority, ok := reports.(billingstore.OperatorCursorAuthority); ok {
			reader.Authority = authority
		}
		out.Allowances = reader
	}
	return out
}

// statementObservationRefReader is the narrow exact-observation capability the
// metering journal already exposes. It is asserted structurally so no concrete
// journal type crosses this composition boundary.
type statementObservationRefReader interface {
	GetObservationRef(context.Context, metering.ObservationRef) (metering.Observation, error)
}

// statementObservationSource adapts an exact journal observation reader to the
// core consumer-owned statement evidence port. A structurally absent reader
// yields nil so the core wrapper records an explicit unavailable state instead
// of silently omitting referenced observations. When the billing reports store
// also implements the retained statement-line reader, it is composed so the
// core can resolve the exact persisted statement-line outcome.
func statementObservationSource(querier metering.Querier, reports billing.ReportingStore) billing.EconomicDetailStatementSource {
	if querier == nil {
		return nil
	}
	reader, ok := querier.(statementObservationRefReader)
	if !ok || reader == nil {
		return nil
	}
	source := journalStatementObservationSource{reader: reader}
	if reports != nil {
		if lineReader, ok := reports.(economics.StatementLineReader); ok && lineReader != nil {
			source.lineReader = lineReader
		}
	}
	return source
}

type journalStatementObservationSource struct {
	reader     statementObservationRefReader
	lineReader economics.StatementLineReader
}

func (s journalStatementObservationSource) GetStatementObservation(ctx context.Context, ref metering.ObservationRef) (metering.Observation, error) {
	observation, err := s.reader.GetObservationRef(ctx, ref)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return metering.Observation{}, billing.ErrEconomicDetailStatementObservationMissing
		}
		return metering.Observation{}, fmt.Errorf("%w: %v", billing.ErrEconomicDetailStatementSourceUnavailable, err)
	}
	return observation, nil
}

// GetStatementLineRevisions resolves the bounded persisted statement-line
// revisions for one verified observation's statement-line identity. The read is
// tenant- and provider-account-scoped and returns nothing rather than guessing
// when the identity is incomplete or the ledger is absent.
func (s journalStatementObservationSource) GetStatementLineRevisions(ctx context.Context, observation metering.Observation) ([]economics.StatementLineView, error) {
	if s.lineReader == nil {
		return nil, nil
	}
	subject := observation.Subject
	if subject.TenantID == "" || subject.ProviderAccountKey == "" || subject.StatementID == "" || subject.StatementLineID == "" {
		return nil, nil
	}
	page, err := s.lineReader.QueryStatementLines(ctx, economics.StatementLineQuery{
		Scope:              economics.OperatorScope{StoreID: subject.StoreID, TenantID: subject.TenantID},
		ProviderAccountKey: subject.ProviderAccountKey,
		StatementID:        subject.StatementID,
		PeriodID:           subject.PeriodID,
		LineID:             subject.StatementLineID,
		Limit:              economics.OperatorPageMaxLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("billingstore: statement line read: %w", err)
	}
	return page.Lines, nil
}
