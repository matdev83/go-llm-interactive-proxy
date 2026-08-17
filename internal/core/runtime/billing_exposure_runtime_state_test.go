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
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	state := newBillingCallState(callID)
	state.noteAllocatedBLeg("b-1", 1)
	got := state.freezeAllocatedBLegs()
	if len(got) != 1 || got[0] != "b-1" {
		t.Fatalf("frozen terminal leg set = %v", got)
	}
}
