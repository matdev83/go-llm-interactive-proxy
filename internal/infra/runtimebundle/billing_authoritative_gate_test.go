package runtimebundle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

type gateStubSink struct{}

func (gateStubSink) AppendCall(context.Context, billing.CallUsageRecord) error   { return nil }
func (gateStubSink) AppendLeg(context.Context, billing.CallLegUsageRecord) error { return nil }

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
	prod.BillingTerminalUsageSink = gateStubSink{}
	if err := requireAuthoritativeBillingPorts(prod, NewResourceLedger()); err != nil {
		t.Fatalf("complete ports: %v", err)
	}
}

func TestRequireStableSpoolPathRejectsVolatileLocations(t *testing.T) {
	t.Parallel()
	// A literal stable state path, provably outside every candidate temp root
	// (the guard is prefix-based and does not require the path to exist).
	stable := filepath.Join(string(filepath.Separator)+"var", "lib", "lip", "state", "spool.db")
	if err := requireStableSpoolPath(stable); err != nil {
		t.Fatalf("stable path %q rejected: %v", stable, err)
	}
	for _, volatile := range []string{os.TempDir(), filepath.Join(os.TempDir(), "spool.db"), "/tmp/spool.db", "/var/tmp/x"} {
		if err := requireStableSpoolPath(volatile); err == nil {
			t.Fatalf("volatile path %q accepted", volatile)
		}
	}
}
