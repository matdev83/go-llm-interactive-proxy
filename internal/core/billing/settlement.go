package billing

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrSettlementInvalid  = errors.New("billing: invalid settlement")
	ErrSettlementConflict = errors.New("billing: settlement replay conflict")
)

// CustomerSettlementSourceKey is the only customer-revenue source identity for
// one sealed A-leg. It deliberately excludes retry labels and wall-clock data.
func CustomerSettlementSourceKey(turKey string) (string, error) {
	if strings.TrimSpace(turKey) == "" {
		return "", fmt.Errorf("%w: TUR key is required", ErrSettlementInvalid)
	}
	return "customer-settlement:v1:" + strings.TrimSpace(turKey), nil
}

// ProviderCostSourceKey is the only provider-COGS source identity for one LUR.
func ProviderCostSourceKey(lurKey string) (string, error) {
	if strings.TrimSpace(lurKey) == "" {
		return "", fmt.Errorf("%w: LUR key is required", ErrSettlementInvalid)
	}
	return "provider-cost:v1:" + strings.TrimSpace(lurKey), nil
}

// ApplyBillingInput is the immutable evidence, bound authorization, and pure
// rating result handed to the durable settlement boundary.
type ApplyBillingInput struct {
	Record        TurnUsageRecord
	Authorization Authorization
	Result        Result
}

// Settlement is the durable outcome of one atomic account settlement. A zero
// customer/provider amount has no JournalTransaction because posted entries are
// strictly positive, but the enclosing operation identity remains auditable.
type Settlement struct {
	TURKey               string
	Customer             Posting
	ProviderCosts        []Posting
	AuthorizationRelease Posting
	Replayed             bool
}

// SettlementStore is the narrow post-turn write boundary. Bun details remain in
// infra/billingstore and never cross this core contract.
type SettlementStore interface {
	ApplyBillingResult(context.Context, ApplyBillingInput) (Settlement, error)
}

func (c OperatorCostResult) SemanticFingerprint() (string, error) {
	if strings.TrimSpace(c.LURKey) == "" || c.Amount.Nano < 0 || strings.TrimSpace(c.Amount.Currency) == "" {
		return "", fmt.Errorf("%w: operator cost identity and amount are required", ErrSettlementInvalid)
	}
	canonical := struct {
		Version       string
		LURKey        string
		Amount        Money
		AmountPresent bool
		Reconciled    bool
		Authoritative bool
		Reason        string
	}{"operator-cost:v1", c.LURKey, c.Amount, c.AmountPresent, c.Reconciled, c.Authoritative, c.UnreconciledReason}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("operator-cost:v1:%x", digest[:]), nil
}

// SemanticFingerprint hashes every financially meaningful result field. It is
// separate from a journal fingerprint because one Result spans several
// durable transactions.
func (r Result) SemanticFingerprint() (string, error) {
	if strings.TrimSpace(r.TURKey) == "" || r.CustomerCharge.Nano < 0 || strings.TrimSpace(r.CustomerCharge.Currency) == "" {
		return "", fmt.Errorf("%w: result identity and customer amount are required", ErrSettlementInvalid)
	}
	type cost struct {
		LURKey        string
		Nano          int64
		Currency      string
		AmountPresent bool
		Reconciled    bool
		Authoritative bool
		Reason        string
	}
	canonical := struct {
		Version          string
		TURKey           string
		Customer         Money
		Costs            []cost
		Unreconciled     bool
		UnreconciledKeys []string
	}{
		Version: "billing-result:v1", TURKey: r.TURKey, Customer: r.CustomerCharge,
		Unreconciled: r.UnreconciledCost, UnreconciledKeys: append([]string(nil), r.UnreconciledLURKeys...),
	}
	canonical.Costs = make([]cost, 0, len(r.OperatorCosts))
	for _, c := range r.OperatorCosts {
		canonical.Costs = append(canonical.Costs, cost{LURKey: c.LURKey, Nano: c.Amount.Nano, Currency: c.Amount.Currency, AmountPresent: c.AmountPresent, Reconciled: c.Reconciled, Authoritative: c.Authoritative, Reason: c.UnreconciledReason})
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("billing-result:v1:%x", digest[:]), nil
}

// Validate proves the settlement boundary is complete before any database
// mutation. In particular, an absent/unreconciled provider cost can never be
// silently represented as zero COGS.
func (in ApplyBillingInput) Validate() error {
	record, err := in.Record.Seal()
	if err != nil {
		return fmt.Errorf("%w: record: %v", ErrSettlementInvalid, err)
	}
	if in.Record.Key != "" {
		if err := CheckReplay(in.Record, record); err != nil {
			return fmt.Errorf("%w: record is not sealed: %v", ErrSettlementInvalid, err)
		}
	}
	if in.Result.TURKey != record.Key {
		return fmt.Errorf("%w: result TUR key does not match record", ErrSettlementInvalid)
	}
	if in.Authorization.ID == "" || in.Authorization.AccountID != record.AccountID || in.Authorization.TURKey != record.Key || in.Authorization.ID != record.AuthorizationID {
		return fmt.Errorf("%w: authorization is not bound to record", ErrSettlementInvalid)
	}
	if err := in.Authorization.Amount.Validate(); err != nil {
		return fmt.Errorf("%w: authorization amount: %v", ErrSettlementInvalid, err)
	}
	if in.Authorization.Amount.Nano < 0 || in.Result.CustomerCharge.Nano < 0 {
		return fmt.Errorf("%w: negative settlement amount", ErrSettlementInvalid)
	}
	if in.Result.CustomerCharge.Currency != in.Authorization.Amount.Currency {
		return fmt.Errorf("%w: customer currency mismatch", ErrSettlementInvalid)
	}
	if in.Result.CustomerCharge.Nano > in.Authorization.Amount.Nano {
		return fmt.Errorf("%w: customer charge exceeds authorization", ErrSettlementInvalid)
	}
	if in.Result.UnreconciledCost || len(in.Result.UnreconciledLURKeys) > 0 {
		return fmt.Errorf("%w: provider cost is unreconciled", ErrSettlementInvalid)
	}
	if len(in.Result.OperatorCosts) != len(record.Legs) {
		return fmt.Errorf("%w: one operator cost is required per LUR", ErrSettlementInvalid)
	}
	seen := make(map[string]struct{}, len(in.Result.OperatorCosts))
	for _, cost := range in.Result.OperatorCosts {
		if strings.TrimSpace(cost.LURKey) == "" {
			return fmt.Errorf("%w: operator LUR key is required", ErrSettlementInvalid)
		}
		if _, ok := seen[cost.LURKey]; ok {
			return fmt.Errorf("%w: duplicate operator LUR key %q", ErrSettlementInvalid, cost.LURKey)
		}
		seen[cost.LURKey] = struct{}{}
		if cost.Amount.Nano < 0 || !cost.AmountPresent || !cost.Reconciled || cost.Amount.Currency != in.Authorization.Amount.Currency {
			return fmt.Errorf("%w: operator cost for %q is not reconciled", ErrSettlementInvalid, cost.LURKey)
		}
	}
	for _, leg := range record.Legs {
		if _, ok := seen[leg.Key]; !ok {
			return fmt.Errorf("%w: operator cost missing for LUR %q", ErrSettlementInvalid, leg.Key)
		}
	}
	return nil
}
