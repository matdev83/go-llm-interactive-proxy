package billing

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

func (c OperatorCostResult) SemanticFingerprint() (string, error) {
	if strings.TrimSpace(c.LURKey) == "" || c.Amount.Nano < 0 || strings.TrimSpace(c.Amount.Currency) == "" {
		return "", fmt.Errorf("%w: provider-cost identity and amount are required", ErrSettlementInvalid)
	}
	canonical := struct {
		Version       string
		LURKey        string
		Amount        Money
		AmountPresent bool
		Reconciled    bool
		Authoritative bool
		Reason        string
	}{"provider-cost:v1", c.LURKey, c.Amount, c.AmountPresent, c.Reconciled, c.Authoritative, c.UnreconciledReason}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("provider-cost:v1:%x", digest[:]), nil
}
