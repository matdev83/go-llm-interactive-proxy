package billing

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AdmissionRequest is the provider-neutral application input for one hard
// monetary admission. Route/model alternatives and immutable economic data are
// supplied by the caller after side-effect-free planning.
type AdmissionRequest struct {
	AccountID       string
	TURKey          string
	AuthorizationID string
	Estimate        MaxChargeInput
	ExpiresAt       time.Time
}

// AdmissionService owns the authorize-before-execute policy without knowing
// Bun, SQL, runtime streams, or provider SDKs.
type AdmissionService struct {
	Store AuthorizationStore
}

func (s *AdmissionService) Authorize(ctx context.Context, request AdmissionRequest) (Authorization, MaxCostBound, error) {
	if s == nil || s.Store == nil {
		return Authorization{}, MaxCostBound{}, fmt.Errorf("%w: authorization store is required", ErrAuthorizationUnavailable)
	}
	request.AccountID = strings.TrimSpace(request.AccountID)
	request.TURKey = strings.TrimSpace(request.TURKey)
	request.AuthorizationID = strings.TrimSpace(request.AuthorizationID)
	if request.AccountID == "" || request.TURKey == "" || request.AuthorizationID == "" {
		return Authorization{}, MaxCostBound{}, fmt.Errorf("%w: account, TUR, and authorization identities are required", ErrAuthorizationInvalid)
	}
	bound, err := EstimateMaxCustomerCharge(request.Estimate)
	if err != nil {
		return Authorization{}, MaxCostBound{}, err
	}
	authorization, err := s.Store.Authorize(ctx, AuthorizeInput{
		ID: request.AuthorizationID, AccountID: request.AccountID, TURKey: request.TURKey,
		MaxCustomerCharge: bound, Amount: bound.Amount,
		PricingRef: bound.PricingRef, ChargePolicyRef: bound.ChargePolicyRef,
		ExpiresAt: request.ExpiresAt,
	})
	if err != nil {
		return Authorization{}, bound, err
	}
	return authorization, bound, nil
}
