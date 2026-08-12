package billing

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAuthorizationAssertOpenForAdmission(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	open := Authorization{Status: HoldStatusOpen, ExpiresAt: now.Add(time.Minute)}
	if err := open.AssertOpenForAdmission(now); err != nil {
		t.Fatalf("open hold: %v", err)
	}
	closed := Authorization{Status: HoldStatusClosed, ExpiresAt: now.Add(time.Minute)}
	if !errors.Is(closed.AssertOpenForAdmission(now), ErrAuthorizationClosed) {
		t.Fatalf("closed = %v", closed.AssertOpenForAdmission(now))
	}
	expired := Authorization{Status: HoldStatusOpen, ExpiresAt: now.Add(-time.Second)}
	if !errors.Is(expired.AssertOpenForAdmission(now), ErrAuthorizationExpired) {
		t.Fatalf("expired = %v", expired.AssertOpenForAdmission(now))
	}
}

func TestAuthorizationInputBindsBoundSnapshotsAndReplaysSemantically(t *testing.T) {
	t.Parallel()
	bound := MaxCostBound{
		Amount:          Money{Nano: 25, Currency: "USD"},
		PricingRef:      VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef: VersionRef{ID: "policy", Version: "v2"},
	}
	in := AuthorizeInput{ID: "auth-1", AccountID: "acct-1", TURKey: "acct-1:turn-1", MaxCustomerCharge: bound}
	sealed, err := in.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Fingerprint == "" || sealed.Amount != bound.Amount {
		t.Fatalf("sealed authorization = %+v", sealed)
	}
	if err := CheckAuthorizationReplay(sealed, in); err != nil {
		t.Fatalf("same replay: %v", err)
	}
	in.Amount = Money{Nano: 26, Currency: "USD"}
	if !errors.Is(CheckAuthorizationReplay(sealed, in), ErrAuthorizationInvalid) {
		t.Fatalf("different amount should fail validation")
	}
}

func TestAuthorizationHoldKeyIsUnambiguous(t *testing.T) {
	t.Parallel()
	first, err := AuthorizationHoldKey("a:b", "c")
	if err != nil {
		t.Fatal(err)
	}
	second, err := AuthorizationHoldKey("a", "b:c")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("hold keys collide: %q", first)
	}
}

func TestAuthorizationInputRejectsReferenceMismatch(t *testing.T) {
	t.Parallel()
	in := AuthorizeInput{
		ID: "a", AccountID: "acct", TURKey: "acct:turn",
		Amount:            Money{Nano: 1, Currency: "USD"},
		MaxCustomerCharge: MaxCostBound{Amount: Money{Nano: 1, Currency: "USD"}, PricingRef: VersionRef{ID: "p", Version: "1"}, ChargePolicyRef: VersionRef{ID: "c", Version: "1"}},
		PricingRef:        VersionRef{ID: "p", Version: "2"}, ChargePolicyRef: VersionRef{ID: "c", Version: "1"},
	}
	if !errors.Is(in.Validate(), ErrAuthorizationInvalid) {
		t.Fatalf("reference mismatch error = %v", in.Validate())
	}
}

func TestAuthorizationStorePortIsContextBased(t *testing.T) {
	t.Parallel()
	var _ AuthorizationStore = authorizationStoreFunc(func(context.Context, AuthorizeInput) (Authorization, error) { return Authorization{}, nil })
}

type authorizationStoreFunc func(context.Context, AuthorizeInput) (Authorization, error)

func (f authorizationStoreFunc) Authorize(ctx context.Context, in AuthorizeInput) (Authorization, error) {
	return f(ctx, in)
}
