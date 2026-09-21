package runtimebundle

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 16.2B composition contract: the protected operator routes receive
// readers only from stores that actually implement the capability ports.
// Stores without a port yield nil members so routes report disabled, and
// stock composition never opts into the normalized statement import.

type operatorFakeStore struct{}

func (operatorFakeStore) AccountReport(context.Context, string, billing.PageRequest) (billing.AccountReport, error) {
	return billing.AccountReport{}, nil
}

func (operatorFakeStore) CallExplanation(context.Context, string) (billing.CallExplanation, error) {
	return billing.CallExplanation{}, nil
}

func (operatorFakeStore) OperatorCostReport(context.Context, billing.ReportFilter) (billing.OperatorCostReport, error) {
	return billing.OperatorCostReport{}, nil
}

func (operatorFakeStore) TrialBalanceReport(context.Context, billing.ReportFilter) (billing.TrialBalanceReport, error) {
	return billing.TrialBalanceReport{}, nil
}

func (operatorFakeStore) QueryOpenExposures(context.Context, string, billing.PageRequest) (billing.ExposurePage, error) {
	return billing.ExposurePage{}, nil
}

func (operatorFakeStore) QueryReconcileRequired(context.Context, billing.PageRequest) (billing.AccountStatePage, error) {
	return billing.AccountStatePage{}, nil
}

func (operatorFakeStore) QueryEconomicDetail(context.Context, billing.EconomicDetailQuery) (billing.EconomicDetail, error) {
	return billing.EconomicDetail{}, nil
}

func (operatorFakeStore) QueryDiscrepancies(context.Context, economics.DiscrepancyQuery) (economics.DiscrepancyPage, error) {
	return economics.DiscrepancyPage{}, nil
}

func (operatorFakeStore) QueryStatementLines(context.Context, economics.StatementLineQuery) (economics.StatementLinePage, error) {
	return economics.StatementLinePage{}, nil
}

func (operatorFakeStore) QueryAdjustments(context.Context, economics.AdjustmentQuery) (economics.AdjustmentPage, error) {
	return economics.AdjustmentPage{}, nil
}

// OperatorCursorAuthority: the composed allowance reader must reuse the
// billing store's durable cursor authority rather than a second secret.
func (operatorFakeStore) DecodeAllowanceCursor(raw, _, _ string) (string, error) {
	return raw, nil
}

func (operatorFakeStore) EncodeAllowanceCursor(_, _, source string) string {
	return source
}

type operatorFakeJournal struct{}

func (operatorFakeJournal) List(ctx context.Context, q metering.Query) (metering.Page, error) {
	return metering.Page{}, nil
}

func (operatorFakeJournal) ListAccountWindowObservations(context.Context, coremetering.AccountWindowQuery) (coremetering.AccountWindowObservationPage, error) {
	return coremetering.AccountWindowObservationPage{}, nil
}

func TestOperatorReportsForMountWiresCapableStore(t *testing.T) {
	t.Parallel()
	got := operatorReportsForMount(operatorFakeStore{}, operatorFakeJournal{})
	if got.EconomicDetail == nil {
		t.Fatal("EconomicDetail must be wired from a capable store")
	}
	if got.Discrepancies == nil {
		t.Fatal("Discrepancies must be wired from a capable store")
	}
	if got.StatementLines == nil {
		t.Fatal("StatementLines must be wired from a capable store")
	}
	if got.Adjustments == nil {
		t.Fatal("Adjustments must be wired from a capable store")
	}
	if got.Allowances == nil {
		t.Fatal("Allowances must be wired from a journal-backed source")
	}
	allowance, ok := got.Allowances.(billingstore.AllowanceReader)
	if !ok {
		t.Fatalf("Allowances = %T, want billingstore.AllowanceReader", got.Allowances)
	}
	if allowance.Authority == nil {
		t.Fatal("Allowances must carry the composed durable cursor authority")
	}
	if got.StatementImport != nil {
		t.Fatal("stock composition must never opt into statement import")
	}
}

func TestOperatorReportsForMountLeavesImportAbsent(t *testing.T) {
	t.Parallel()
	got := operatorReportsForMount(operatorFakeStore{}, nil)
	if got.StatementImport != nil {
		t.Fatal("StatementImport must stay nil without explicit host opt-in")
	}
	if got.Allowances != nil {
		t.Fatal("Allowances must stay nil without a journal-backed source")
	}
	if got.EconomicDetail == nil || got.Discrepancies == nil || got.StatementLines == nil || got.Adjustments == nil {
		t.Fatal("billingstore-native readers must be wired even without a journal")
	}
}

func TestOperatorReportsForMountNilStoreDisablesAll(t *testing.T) {
	t.Parallel()
	got := operatorReportsForMount(nil, nil)
	if got.EconomicDetail != nil || got.Discrepancies != nil || got.Allowances != nil ||
		got.StatementLines != nil || got.Adjustments != nil || got.StatementImport != nil {
		t.Fatalf("nil store must disable every operator route: %+v", got)
	}
}

var (
	_ billing.ReportingStore               = operatorFakeStore{}
	_ billing.EconomicDetailReader         = operatorFakeStore{}
	_ economics.DiscrepancyReader          = operatorFakeStore{}
	_ economics.StatementLineReader        = operatorFakeStore{}
	_ economics.AdjustmentReader           = operatorFakeStore{}
	_ billingstore.OperatorCursorAuthority = operatorFakeStore{}
	_ billingstore.AccountWindowSource     = operatorFakeJournal{}
)

var (
	_ billing.EconomicDetailReader  = (*billingstore.DurableStore)(nil)
	_ economics.DiscrepancyReader   = (*billingstore.DurableStore)(nil)
	_ economics.StatementLineReader = (*billingstore.DurableStore)(nil)
	_ economics.AdjustmentReader    = (*billingstore.DurableStore)(nil)
	_ economics.AllowanceReader     = billingstore.AllowanceReader{}
)
