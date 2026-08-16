package billing

import (
	"errors"
	"strings"
	"testing"
)

func TestNewBillingCallIDIsProxyOwnedAndUnique(t *testing.T) {
	t.Parallel()
	first, err := NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("generated id must be valid: %v", err)
	}
	second, err := NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("each incoming billable invocation must receive a distinct BillingCallID")
	}
	if first.String() == "" || strings.ContainsAny(first.String(), " \t\n") {
		t.Fatalf("BillingCallID must be a non-empty opaque token, got %q", first)
	}
}

func TestParseBillingCallIDRejectsALegAndSessionIdentity(t *testing.T) {
	t.Parallel()
	valid, err := NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseBillingCallID(valid.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed != valid {
		t.Fatalf("ParseBillingCallID = %q, want %q", parsed, valid)
	}

	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "whitespace", raw: "  "},
		{name: "A-leg id is correlation not settlement", raw: "a_0123456789abcdef0123456789abcdef"},
		{name: "B-leg id is not a billing call id", raw: "b_0123456789abcdef0123456789abcdef"},
		{name: "session id is reporting only", raw: "sess_long_lived_authoritative"},
		{name: "client call id is not proxy billing identity", raw: "call_client_hint"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseBillingCallID(tt.raw); !errors.Is(err, ErrBillingCallIDInvalid) {
				t.Fatalf("ParseBillingCallID(%q) = %v, want ErrBillingCallIDInvalid", tt.raw, err)
			}
		})
	}
}

func TestCustomerOperationKeyIsAccountPlusBillingCallID(t *testing.T) {
	t.Parallel()
	callID, err := NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	key, err := NewCustomerOperationKey("acct", callID)
	if err != nil {
		t.Fatal(err)
	}
	if key.AccountID != "acct" || key.CallID != callID {
		t.Fatalf("customer key = %+v", key)
	}
	encoded := key.String()
	if !strings.Contains(encoded, "acct") || !strings.Contains(encoded, callID.String()) {
		t.Fatalf("customer key string %q must include account and BillingCallID", encoded)
	}
	if strings.Contains(encoded, "a_shared") || strings.Contains(encoded, "sess_shared") {
		t.Fatal("customer settlement key must not include A-leg or session identity")
	}

	if _, err := NewCustomerOperationKey("", callID); !errors.Is(err, ErrBillingCallIDInvalid) && !errors.Is(err, ErrSettlementInvalid) {
		t.Fatalf("empty account = %v, want identity/settlement invalid", err)
	}
	if _, err := NewCustomerOperationKey("acct", BillingCallID("a_0123456789abcdef0123456789abcdef")); !errors.Is(err, ErrBillingCallIDInvalid) {
		t.Fatalf("A-leg as BillingCallID = %v, want ErrBillingCallIDInvalid", err)
	}
	if _, err := NewCustomerOperationKey("acct", BillingCallID("sess_shared")); !errors.Is(err, ErrBillingCallIDInvalid) {
		t.Fatalf("session as BillingCallID = %v, want ErrBillingCallIDInvalid", err)
	}
}

func TestProviderCostKeyIsBillingCallIDPlusBLeg(t *testing.T) {
	t.Parallel()
	callID, err := NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewProviderCostOperationKey(callID, "b_fail")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := NewProviderCostOperationKey(callID, "b_retry")
	if err != nil {
		t.Fatal(err)
	}
	parallel, err := NewProviderCostOperationKey(callID, "b_par")
	if err != nil {
		t.Fatal(err)
	}
	if first.CallID != callID || retry.CallID != callID || parallel.CallID != callID {
		t.Fatal("retries, failover alternatives, and parallel B-legs must share the incoming BillingCallID")
	}
	seen := map[ProviderCostOperationKey]struct{}{first: {}, retry: {}, parallel: {}}
	if len(seen) != 3 {
		t.Fatal("each B-leg usage identity must be unique for BillingCallID + B-leg")
	}

	if _, err := NewProviderCostOperationKey(callID, ""); !errors.Is(err, ErrBillingCallIDInvalid) && !errors.Is(err, ErrSettlementInvalid) {
		t.Fatalf("empty B-leg = %v, want identity/settlement invalid", err)
	}
	if _, err := NewProviderCostOperationKey("", "b_1"); !errors.Is(err, ErrBillingCallIDInvalid) {
		t.Fatalf("empty BillingCallID = %v, want ErrBillingCallIDInvalid", err)
	}
}

func TestReuseOfOneALegAndSessionProducesDistinctCustomerOperations(t *testing.T) {
	t.Parallel()
	const account = "acct"
	const aLeg = "a_0123456789abcdef0123456789abcdef"
	const session = "sess_long_lived"
	firstCall, err := NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	secondCall, err := NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	if firstCall == secondCall {
		t.Fatal("later calls on the same A-leg/session must allocate a new BillingCallID")
	}
	first, err := NewCustomerOperationKey(account, firstCall)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewCustomerOperationKey(account, secondCall)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("customer billing operations must be distinct per BillingCallID even when A-leg and session are reused")
	}
	if strings.Contains(first.String(), aLeg) || strings.Contains(second.String(), session) {
		t.Fatal("A-leg/session remain correlation only and must not be the settlement key")
	}
}
