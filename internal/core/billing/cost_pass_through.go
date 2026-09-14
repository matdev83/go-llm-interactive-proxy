package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// CostPassThroughMissingCostPolicy controls the bounded customer outcome when
// the authoritative provider cost is not available at call settlement. The
// policy is part of the frozen retail offer; provider readiness never selects
// this basis implicitly.
type CostPassThroughMissingCostPolicy string

const (
	CostPassThroughMissingCostPending     CostPassThroughMissingCostPolicy = "pending"
	CostPassThroughMissingCostProvisional CostPassThroughMissingCostPolicy = "provisional"
)

// CostPassThroughSettlementStatus is the customer-side state retained with a
// cost-pass-through settlement. Pending has no monetary posting, provisional
// posts the approved bound, and final posts either the authoritative cost or a
// bound whose policy explicitly forbids later adjustment.
type CostPassThroughSettlementStatus string

const (
	CostPassThroughSettlementPending     CostPassThroughSettlementStatus = "pending"
	CostPassThroughSettlementProvisional CostPassThroughSettlementStatus = "provisional"
	CostPassThroughSettlementFinal       CostPassThroughSettlementStatus = "final"
)

var (
	ErrCostPassThroughPolicyInvalid      = errors.New("billing: invalid cost pass-through policy")
	ErrCostPassThroughProviderIncomplete = errors.New("billing: cost pass-through provider cost is incomplete")
	ErrCostPassThroughProviderUntrusted  = errors.New("billing: cost pass-through provider cost is untrusted")
	ErrCostPassThroughCurrencyMismatch   = errors.New("billing: cost pass-through currency is incomparable")
	ErrCostPassThroughBoundExceeded      = errors.New("billing: cost pass-through amount exceeds approved bound")
	ErrCostPassThroughSettlementInvalid  = errors.New("billing: invalid cost pass-through settlement")
	ErrCostPassThroughRevisionConflict   = errors.New("billing: cost pass-through revision conflicts")
	ErrCostPassThroughLateAdjustment     = errors.New("billing: late cost pass-through adjustment is not permitted")
	ErrCostPassThroughHeadNotFound       = errors.New("billing: cost pass-through settlement head not found")
)

// CostPassThroughPolicy is the explicit customer offer for provider-cost
// pass-through. SafeBound is mandatory for direct rating and is the maximum
// customer amount that admission/settlement may accept. RateCall may fill it
// from its already-admitted MaxCustomerCharge when the policy is supplied by
// a legacy catalog adapter.
type CostPassThroughPolicy struct {
	MissingCost         CostPassThroughMissingCostPolicy
	SafeBound           *Money
	AllowLateAdjustment bool
}

func (p CostPassThroughPolicy) Clone() CostPassThroughPolicy {
	out := p
	if p.SafeBound != nil {
		bound := *p.SafeBound
		out.SafeBound = &bound
	}
	return out
}

func (p CostPassThroughPolicy) Validate() error {
	switch p.MissingCost {
	case CostPassThroughMissingCostPending, CostPassThroughMissingCostProvisional:
	default:
		return fmt.Errorf("%w: unsupported missing-cost mode %q", ErrCostPassThroughPolicyInvalid, p.MissingCost)
	}
	if p.SafeBound == nil {
		return fmt.Errorf("%w: safe bound is required", ErrCostPassThroughPolicyInvalid)
	}
	if err := p.SafeBound.Validate(); err != nil {
		return fmt.Errorf("%w: safe bound: %w", ErrCostPassThroughPolicyInvalid, err)
	}
	if p.SafeBound.Nano < 0 {
		return fmt.Errorf("%w: safe bound cannot be negative", ErrCostPassThroughPolicyInvalid)
	}
	return nil
}

// CostPassThroughProviderCost is a normalized, source-qualified provider
// amount. It is accepted only when the amount is present, reconciled and
// authoritative. InputHash is the immutable upstream valuation-input hash;
// it participates in replay/conflict identity but never substitutes for
// provider authority.
type CostPassThroughProviderCost struct {
	LURKey        string
	ValuationID   string
	Revision      uint64
	InputHash     string
	Amount        Money
	AmountPresent bool
	Reconciled    bool
	Authoritative bool
}

// ProviderCost is a short compatibility alias for callers that already use a
// provider-cost vocabulary.
type ProviderCost = CostPassThroughProviderCost

func (p CostPassThroughProviderCost) Clone() CostPassThroughProviderCost { return p }

func (p CostPassThroughProviderCost) Validate(expectedCurrency string) error {
	if strings.TrimSpace(p.LURKey) == "" || strings.TrimSpace(p.ValuationID) == "" || p.Revision == 0 {
		return fmt.Errorf("%w: LUR key, valuation identity and positive revision are required", ErrCostPassThroughProviderIncomplete)
	}
	if !p.AmountPresent {
		return fmt.Errorf("%w: amount is absent", ErrCostPassThroughProviderIncomplete)
	}
	if err := p.Amount.Validate(); err != nil {
		return fmt.Errorf("%w: amount: %w", ErrCostPassThroughProviderIncomplete, err)
	}
	if p.Amount.Nano < 0 {
		return fmt.Errorf("%w: amount cannot be negative", ErrCostPassThroughProviderIncomplete)
	}
	if len(p.InputHash) != sha256.Size*2 {
		return fmt.Errorf("%w: input hash must be a SHA-256 hex digest", ErrCostPassThroughProviderIncomplete)
	}
	if _, err := hex.DecodeString(p.InputHash); err != nil {
		return fmt.Errorf("%w: input hash: %w", ErrCostPassThroughProviderIncomplete, err)
	}
	if !p.Reconciled || !p.Authoritative {
		return fmt.Errorf("%w: accepted cost must be reconciled and authoritative", ErrCostPassThroughProviderUntrusted)
	}
	if expectedCurrency != "" && p.Amount.Currency != expectedCurrency {
		return fmt.Errorf("%w: provider %q versus customer %q", ErrCostPassThroughCurrencyMismatch, p.Amount.Currency, expectedCurrency)
	}
	return nil
}

func (p CostPassThroughProviderCost) SemanticFingerprint() (string, error) {
	if err := p.Validate(""); err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		Version       string
		LURKey        string
		ValuationID   string
		Revision      uint64
		InputHash     string
		Amount        Money
		AmountPresent bool
		Reconciled    bool
		Authoritative bool
	}{
		Version: "cost-pass-through-provider-cost:v1", LURKey: strings.TrimSpace(p.LURKey),
		ValuationID: strings.TrimSpace(p.ValuationID), Revision: p.Revision, InputHash: strings.ToLower(p.InputHash),
		Amount: p.Amount, AmountPresent: p.AmountPresent, Reconciled: p.Reconciled, Authoritative: p.Authoritative,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return "cost-pass-through-provider-cost:v1:" + hex.EncodeToString(digest[:]), nil
}

// CostPassThroughSettlement is frozen customer settlement metadata. The
// store persists it beside the customer operation and uses PostedAmount as
// the current head for subsequent revision deltas.
type CostPassThroughSettlement struct {
	PolicyRef             VersionRef
	Policy                CostPassThroughPolicy
	Status                CostPassThroughSettlementStatus
	SafeBound             Money
	PostedAmount          Money
	ProviderCost          *CostPassThroughProviderCost
	OriginalTransactionID string
}

func (s CostPassThroughSettlement) Clone() CostPassThroughSettlement {
	out := s
	out.Policy = s.Policy.Clone()
	if s.ProviderCost != nil {
		cost := s.ProviderCost.Clone()
		out.ProviderCost = &cost
	}
	return out
}

func (s CostPassThroughSettlement) Validate(expectedCurrency string) error {
	if err := s.Policy.Validate(); err != nil {
		return fmt.Errorf("%w: policy: %w", ErrCostPassThroughSettlementInvalid, err)
	}
	if s.PolicyRef.ID == "" || s.PolicyRef.Version == "" {
		return fmt.Errorf("%w: policy reference is required", ErrCostPassThroughSettlementInvalid)
	}
	if s.Status != CostPassThroughSettlementPending && s.Status != CostPassThroughSettlementProvisional && s.Status != CostPassThroughSettlementFinal {
		return fmt.Errorf("%w: unsupported settlement status %q", ErrCostPassThroughSettlementInvalid, s.Status)
	}
	if err := s.SafeBound.Validate(); err != nil {
		return fmt.Errorf("%w: safe bound: %w", ErrCostPassThroughSettlementInvalid, err)
	}
	if err := s.PostedAmount.Validate(); err != nil {
		return fmt.Errorf("%w: posted amount: %w", ErrCostPassThroughSettlementInvalid, err)
	}
	if s.SafeBound.Nano < 0 || s.PostedAmount.Nano < 0 || s.PostedAmount.Nano > s.SafeBound.Nano {
		return fmt.Errorf("%w: posted amount is outside safe bound", ErrCostPassThroughSettlementInvalid)
	}
	if s.SafeBound.Currency != s.Policy.SafeBound.Currency || s.SafeBound.Nano != s.Policy.SafeBound.Nano || s.PostedAmount.Currency != s.SafeBound.Currency {
		return fmt.Errorf("%w: settlement currency/bound differs from policy", ErrCostPassThroughSettlementInvalid)
	}
	if expectedCurrency != "" && s.SafeBound.Currency != expectedCurrency {
		return fmt.Errorf("%w: settlement %q versus account %q", ErrCostPassThroughCurrencyMismatch, s.SafeBound.Currency, expectedCurrency)
	}
	if s.Status == CostPassThroughSettlementPending && s.PostedAmount.Nano != 0 {
		return fmt.Errorf("%w: pending settlement must have zero posted amount", ErrCostPassThroughSettlementInvalid)
	}
	if s.Status == CostPassThroughSettlementProvisional && !s.Policy.AllowLateAdjustment {
		return fmt.Errorf("%w: provisional settlement requires late adjustment permission", ErrCostPassThroughSettlementInvalid)
	}
	if s.ProviderCost != nil {
		if err := s.ProviderCost.Validate(s.SafeBound.Currency); err != nil {
			return fmt.Errorf("%w: provider cost: %w", ErrCostPassThroughSettlementInvalid, err)
		}
		if s.ProviderCost.Amount.Nano != s.PostedAmount.Nano {
			return fmt.Errorf("%w: provider cost does not match posted amount", ErrCostPassThroughSettlementInvalid)
		}
		if s.Status != CostPassThroughSettlementFinal {
			return fmt.Errorf("%w: provider-backed settlement must be final", ErrCostPassThroughSettlementInvalid)
		}
	}
	return nil
}

func (s CostPassThroughSettlement) SemanticFingerprint() (string, error) {
	if err := s.Validate(""); err != nil {
		return "", err
	}
	provider := ""
	if s.ProviderCost != nil {
		var err error
		provider, err = s.ProviderCost.SemanticFingerprint()
		if err != nil {
			return "", err
		}
	}
	payload, err := json.Marshal(struct {
		Version             string
		PolicyRef           VersionRef
		Policy              CostPassThroughPolicy
		Status              CostPassThroughSettlementStatus
		SafeBound           Money
		PostedAmount        Money
		ProviderFingerprint string
	}{
		Version: "cost-pass-through-settlement:v1", PolicyRef: s.PolicyRef, Policy: s.Policy,
		Status: s.Status, SafeBound: s.SafeBound, PostedAmount: s.PostedAmount,
		ProviderFingerprint: provider,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return "cost-pass-through-settlement:v1:" + hex.EncodeToString(digest[:]), nil
}

// CostPassThroughRevisionInput is the store-facing late authoritative
// revision. Provider computation and trust checks happen before this atomic
// posting boundary, so no account lock is held while a supplier is queried.
type CostPassThroughRevisionInput struct {
	AccountID    string
	CallID       BillingCallID
	ProviderCost CostPassThroughProviderCost
}

type ApplyCostPassThroughRevisionInput = CostPassThroughRevisionInput

// CostPassThroughRevisionResult reports an idempotent head transition. Delta
// is signed: a negative value credits the customer, while a positive value is
// a new customer debit.
type CostPassThroughRevisionResult struct {
	CallID         BillingCallID
	Status         CostPassThroughSettlementStatus
	PreviousAmount Money
	CurrentAmount  Money
	Delta          Money
	ProviderCost   CostPassThroughProviderCost
	Posting        Posting
	Applied        bool
	Replayed       bool
	Stale          bool
	Ignored        bool
}

type ApplyCostPassThroughRevisionResult = CostPassThroughRevisionResult

const CostPassThroughAdjustmentOperationKind = "customer_cost_pass_through_adjustment"

func (in CostPassThroughRevisionInput) Validate() error {
	if strings.TrimSpace(in.AccountID) == "" {
		return fmt.Errorf("%w: account id is required", ErrCostPassThroughSettlementInvalid)
	}
	if err := in.CallID.Validate(); err != nil {
		return fmt.Errorf("%w: call id: %w", ErrCostPassThroughSettlementInvalid, err)
	}
	if err := in.ProviderCost.Validate(""); err != nil {
		return err
	}
	return nil
}

func CostPassThroughAdjustmentSourceKey(accountID string, callID BillingCallID, provider CostPassThroughProviderCost) (string, error) {
	if err := callID.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(provider.LURKey) == "" || strings.TrimSpace(provider.ValuationID) == "" || provider.Revision == 0 {
		return "", fmt.Errorf("%w: adjustment identity is incomplete", ErrCostPassThroughSettlementInvalid)
	}
	return fmt.Sprintf("cost-pass-through-adjustment:v1:%s:%s:%s:%d:%s", strings.TrimSpace(accountID), callID.String(), strings.TrimSpace(provider.LURKey), provider.Revision, strings.TrimSpace(provider.ValuationID)), nil
}
