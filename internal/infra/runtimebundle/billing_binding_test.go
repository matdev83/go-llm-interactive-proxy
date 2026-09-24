package runtimebundle

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingbinding"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

type stubScreen struct{}

func (stubScreen) Check(context.Context, billing.CreditScreenInput) (billing.CreditScreenResult, error) {
	return billing.CreditScreenResult{Decision: billing.CreditAllow, StoreID: "store-test", AccountID: "acct-1"}, nil
}

type stubQuoter struct{}

func (stubQuoter) Quote(context.Context, economics.QuoteInput) (economics.ExposureQuote, error) {
	return economics.ExposureQuote{}, nil
}

type stubAdmitter struct{}

func (stubAdmitter) Admit(context.Context, billing.ExposureAdmissionInput) (billing.ExposureHandle, error) {
	return billing.ExposureHandle{StoreID: "store-test", BillingCallID: "call-1", ExposureID: "exp-1", QuoteID: "q-1"}, nil
}

type stubTerminal struct{}

func (stubTerminal) AppendTerminal(context.Context, billing.TerminalEnvelope) (billing.TerminalAck, error) {
	return billing.TerminalAck{StoreID: "store-test", EnvelopeID: "env-1", Revision: 1}, nil
}

func testBinding() billing.Binding {
	return billing.Binding{
		ID:      "composition-test",
		Version: billing.BindingVersionV1,
		Scope: billing.BindingScope{
			StoreID: "store-test",
			Tariff:  economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-snap", Version: "v1"}},
			Policy:  economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-snap", Version: "v1"}, PolicyID: "policy-1"},
		},
		CreditScreen: stubScreen{},
		Quoter:       stubQuoter{},
		Admission:    stubAdmitter{},
		Terminal:     stubTerminal{},
	}
}

func TestExternalBillingCompositionUsesSameChokepoints(t *testing.T) {
	t.Parallel()
	adapter, err := billingbinding.NewAdapter(testBinding())
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	var prod ProductionOptions
	gate, admission, sink, identity := adapter.Chokepoints()
	prod.BillingCreditGate = gate
	prod.BillingExposureAdmission = admission
	prod.BillingTerminalUsageSink = sink
	prod.BillingIdentity = identity
	if prod.BillingStore != nil {
		t.Fatal("external binding must not invent an internal store")
	}
	if prod.BillingCreditGate == nil || prod.BillingExposureAdmission == nil || prod.BillingTerminalUsageSink == nil {
		t.Fatal("external binding must occupy the same runtime chokepoints as the reference composition")
	}
	if prod.BillingIdentity.AccountID == nil || prod.BillingIdentity.StoreID == nil {
		t.Fatal("external binding must resolve runtime billing identity")
	}
	if !billingCompositionConfigured(prod) {
		t.Fatal("external binding composition must be detected as billing-configured")
	}
	if !externalBillingBindingConfigured(prod) {
		t.Fatal("external binding mode must be detected without a store")
	}
	if err := requireCompleteBillingComposition(prod); err != nil {
		t.Fatalf("external binding composition must satisfy the complete seam: %v", err)
	}
}

func TestExternalBillingCompositionRejectsPartialPorts(t *testing.T) {
	t.Parallel()
	var prod ProductionOptions
	adapter, err := billingbinding.NewAdapter(testBinding())
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	gate, admission, sink, identity := adapter.Chokepoints()
	_ = gate
	prod.BillingExposureAdmission = admission
	prod.BillingTerminalUsageSink = sink
	prod.BillingIdentity = identity
	_ = gate
	if externalBillingBindingConfigured(prod) {
		t.Fatal("partial external ports must not count as a complete binding")
	}
	if err := requireCompleteBillingComposition(prod); err == nil {
		t.Fatal("partial external ports must fail the complete seam")
	}
}

func TestStockCompositionStaysNonMoney(t *testing.T) {
	t.Parallel()
	var prod ProductionOptions
	if billingCompositionConfigured(prod) {
		t.Fatal("empty production options must remain non-money")
	}
	if externalBillingBindingConfigured(prod) {
		t.Fatal("empty production options must not detect external billing")
	}
}
