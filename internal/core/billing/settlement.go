package billing

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrSettlementInvalid           = errors.New("billing: invalid settlement")
	ErrSettlementConflict          = errors.New("billing: settlement replay conflict")
	ErrSettlementReconcileRequired = errors.New("billing: settlement requires reconciliation")
)

func CustomerSettlementSourceKey(accountID string, callID BillingCallID) (string, error) {
	key, err := NewCustomerOperationKey(accountID, callID)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrSettlementInvalid, err)
	}
	return "customer-settlement:v2:" + key.String(), nil
}

func ProviderCostSourceKey(lurKey string) (string, error) {
	if strings.TrimSpace(lurKey) == "" {
		return "", fmt.Errorf("%w: B-leg usage key is required", ErrSettlementInvalid)
	}
	return "provider-cost:v1:" + strings.TrimSpace(lurKey), nil
}
