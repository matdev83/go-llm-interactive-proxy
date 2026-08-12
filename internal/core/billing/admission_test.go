package billing

import (
	"context"
	"testing"
)

func TestAdmissionServiceEstimatesAndDelegatesAtomicAuthorization(t *testing.T) {
	t.Parallel()
	var got AuthorizeInput
	service := &AdmissionService{Store: authorizationStoreFunc(func(_ context.Context, in AuthorizeInput) (Authorization, error) {
		got = in
		return Authorization{ID: in.ID, AccountID: in.AccountID, TURKey: in.TURKey, Amount: in.Amount}, nil
	})}
	policy := estimatePolicy(ChargeSurfacedTurn)
	_, bound, err := service.Authorize(context.Background(), AdmissionRequest{
		AccountID: "acct", TURKey: "acct:turn", AuthorizationID: "auth",
		Estimate: MaxChargeInput{Currency: "USD", InputTokens: 1, InputTokensPresent: true, Policy: policy, Routes: []ChargeRoute{estimateRouteFixture("route", 1, 1)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Amount != bound.Amount || got.MaxCustomerCharge.PricingRef != policy.PricingRef || got.MaxCustomerCharge.ChargePolicyRef != policy.Ref {
		t.Fatalf("delegated input = %+v bound=%+v", got, bound)
	}
}
