package billing

import "testing"

func TestAuthoritativeBillingDoesNotIncludeAuthorizationLookup(t *testing.T) {
	t.Parallel()
	var store AuthoritativeBilling = (*admissionOnlyBilling)(nil)
	if _, ok := store.(AuthorizationLookup); ok {
		t.Fatal("AuthoritativeBilling must not require AuthorizationLookup")
	}
}
