package runtime

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

type exposureRuntimeStateAdmission struct{}

func (exposureRuntimeStateAdmission) Admit(context.Context, BillingExposureAdmissionInput) (billing.CallExposure, error) {
	return billing.CallExposure{Status: billing.ExposureOpen}, nil
}

func TestExposureGenerationUsesOnlyTerminalCallState(t *testing.T) {
	executor := &Executor{BillingRuntime: BillingRuntime{
		BillingExposureAdmission: exposureRuntimeStateAdmission{},
		CallUsageAppender:        billing.CallUsageAppenderFunc(func(context.Context, billing.CallUsageRecord) error { return nil }),
	}}

	collector := executor.billingTurns()
	if collector == nil {
		t.Fatal("billing call state is required")
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	collector.noteAllocatedBLeg(callID, "b-1")
	got := collector.freezeAllocatedBLegs(callID)
	if len(got) != 1 || got[0] != "b-1" {
		t.Fatalf("frozen terminal leg set = %v", got)
	}
}
