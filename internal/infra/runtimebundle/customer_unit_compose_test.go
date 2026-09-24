package runtimebundle_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)

func TestComposeBillingDiscoversCustomerUnitLedgerFromAuthoritativeStore(t *testing.T) {
	t.Parallel()
	in, store, _, _ := validComposeInput(t)
	wrapped := &completeJournalWithCustomerUnitLedger{completeJournal: *store}
	in.Store = wrapped
	in.TerminalUsageSink = wrapped
	production, err := runtimebundle.ComposeBilling(in)
	if err != nil {
		t.Fatalf("ComposeBilling: %v", err)
	}
	if production.BillingCustomerUnitLedger == nil {
		t.Fatal("BillingCustomerUnitLedger was not discovered from authoritative store")
	}
}

func TestComposeBillingDiscoversOptionalCostPassThroughSettlementStore(t *testing.T) {
	t.Parallel()
	in, store, _, _ := validComposeInput(t)
	wrapped := &completeJournalWithCostPassThrough{completeJournal: *store}
	in.Store = wrapped
	in.TerminalUsageSink = wrapped
	production, err := runtimebundle.ComposeBilling(in)
	if err != nil {
		t.Fatalf("ComposeBilling: %v", err)
	}
	if production.BillingCostPassThroughSettlementStore == nil {
		t.Fatal("BillingCostPassThroughSettlementStore was not discovered from authoritative store")
	}
}

type completeJournalWithCustomerUnitLedger struct {
	completeJournal
}

func (*completeJournalWithCustomerUnitLedger) ApplyCustomerUnitOperation(context.Context, billing.CustomerUnitOperation) (billing.CustomerUnitOperationResult, error) {
	return billing.CustomerUnitOperationResult{}, nil
}

type completeJournalWithCostPassThrough struct {
	completeJournal
}

func (*completeJournalWithCostPassThrough) ApplyCostPassThroughRevision(context.Context, billing.CostPassThroughRevisionInput) (billing.CostPassThroughRevisionResult, error) {
	return billing.CostPassThroughRevisionResult{}, nil
}
