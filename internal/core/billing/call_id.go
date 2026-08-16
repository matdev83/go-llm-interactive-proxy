package billing

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	billingCallIDPrefix = "bc_"
	billingCallIDHexLen = 32
)

var ErrBillingCallIDInvalid = errors.New("billing: invalid billing call id")

type (
	BillingCallID        string
	CustomerOperationKey struct {
		AccountID string
		CallID    BillingCallID
	}
)

type ProviderCostOperationKey struct {
	CallID BillingCallID
	BLegID string
}

func NewBillingCallID() (BillingCallID, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("billing: billing call id entropy: %w", err)
	}
	id := BillingCallID(billingCallIDPrefix + hex.EncodeToString(raw[:]))
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

func ParseBillingCallID(raw string) (BillingCallID, error) {
	id := BillingCallID(strings.TrimSpace(raw))
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}
func (id BillingCallID) String() string { return string(id) }
func (id BillingCallID) Validate() error {
	raw := string(id)
	if strings.TrimSpace(raw) != raw || raw == "" {
		return fmt.Errorf("%w: value is required", ErrBillingCallIDInvalid)
	}
	if !strings.HasPrefix(raw, billingCallIDPrefix) {
		return fmt.Errorf("%w: missing %s prefix", ErrBillingCallIDInvalid, billingCallIDPrefix)
	}
	payload := raw[len(billingCallIDPrefix):]
	if len(payload) != billingCallIDHexLen {
		return fmt.Errorf("%w: unexpected length", ErrBillingCallIDInvalid)
	}
	if _, err := hex.DecodeString(payload); err != nil {
		return fmt.Errorf("%w: %v", ErrBillingCallIDInvalid, err)
	}
	return nil
}

func NewCustomerOperationKey(accountID string, callID BillingCallID) (CustomerOperationKey, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return CustomerOperationKey{}, fmt.Errorf("%w: account id is required", ErrBillingCallIDInvalid)
	}
	if err := callID.Validate(); err != nil {
		return CustomerOperationKey{}, err
	}
	return CustomerOperationKey{AccountID: accountID, CallID: callID}, nil
}

func (k CustomerOperationKey) String() string {
	return k.AccountID + "/" + k.CallID.String()
}

func NewProviderCostOperationKey(callID BillingCallID, bLegID string) (ProviderCostOperationKey, error) {
	if err := callID.Validate(); err != nil {
		return ProviderCostOperationKey{}, err
	}
	bLegID = strings.TrimSpace(bLegID)
	if bLegID == "" {
		return ProviderCostOperationKey{}, fmt.Errorf("%w: b-leg id is required", ErrBillingCallIDInvalid)
	}
	return ProviderCostOperationKey{CallID: callID, BLegID: bLegID}, nil
}

func (k ProviderCostOperationKey) String() string {
	return k.CallID.String() + "/" + k.BLegID
}
