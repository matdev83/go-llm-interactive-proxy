package billing

import "testing"

func TestAuthoritativeBillingDoesNotRequireHoldLifecycle(t *testing.T) {
	t.Parallel()
	var store AuthoritativeBilling = (*admissionOnlyBilling)(nil)
	type authorizePort interface {
		Authorize(any) (any, error)
	}
	if _, ok := any(store).(authorizePort); ok {
		t.Fatal("AuthoritativeBilling must not require Authorize")
	}
}
