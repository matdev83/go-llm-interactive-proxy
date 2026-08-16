package runtimebundle

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type gateStubExposure struct{}

func (gateStubExposure) Admit(context.Context, coreruntime.BillingExposureAdmissionInput) (billing.CallExposure, error) {
	return billing.CallExposure{}, nil
}

type gateStubCredit struct{}

func (gateStubCredit) Check(context.Context, string) error { return nil }

type gateStubStore struct {
	billing.AuthoritativeBilling
}

func TestRequireAuthoritativeBillingPortsNeedsCreditGate(t *testing.T) {
	t.Parallel()
	prod := ProductionOptions{
		BillingStore:             gateStubStore{},
		BillingExposureAdmission: gateStubExposure{},
		BillingIdentity: coreruntime.BillingIdentity{
			AccountID: func(context.Context, lipapi.Call) string { return "acct" },
		},
	}
	if err := requireAuthoritativeBillingPorts(prod, NewResourceLedger()); !errors.Is(err, ErrAuthoritativeBillingRequired) {
		t.Fatalf("missing CreditGate: error = %v, want ErrAuthoritativeBillingRequired", err)
	}
	prod.BillingCreditGate = gateStubCredit{}
	if err := requireAuthoritativeBillingPorts(prod, NewResourceLedger()); err != nil {
		t.Fatalf("complete ports: %v", err)
	}
}
