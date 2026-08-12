package billing

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrAuthorizationInvalid     = errors.New("billing: invalid authorization")
	ErrAuthorizationConflict    = errors.New("billing: authorization replay conflict")
	ErrAuthorizationUnavailable = errors.New("billing: authorization store unavailable")
	ErrAuthorizationClosed      = errors.New("billing: authorization hold is closed")
	ErrAuthorizationExpired     = errors.New("billing: authorization hold expired")
	ErrLegacyAuthorization      = errors.New("billing: legacy authorization requires repair")
)

// HoldStatus is the durable lifecycle of one authorization hold row.
type HoldStatus string

const (
	HoldStatusOpen   HoldStatus = "open"
	HoldStatusClosed HoldStatus = "closed"
)

// AccountSnapshot is redundant point-in-time evidence captured in the same
// transaction as an authorization hold. Journal history remains authoritative.
type AccountSnapshot struct {
	BalanceNano     int64
	ReservedNano    int64
	SpendableNano   int64
	CreditFloorNano int64
	CreditLimitNano int64
	Mode            AccountMode
	Currency        string
	Version         uint64
}

// Authorization is the sealed customer exposure reservation returned by
// admission. It carries exact pricing/policy references for later rating.
type Authorization struct {
	ID              string
	AccountID       string
	TURKey          string
	Amount          Money
	PricingRef      VersionRef
	ChargePolicyRef VersionRef
	ExpiresAt       time.Time
	Fingerprint     string
	Status          HoldStatus
	Before          AccountSnapshot
	After           AccountSnapshot
}

// AssertOpenForAdmission rejects closed or expired holds before upstream work.
// Empty Status is treated as not yet admissible for replay of a durable row.
func (a Authorization) AssertOpenForAdmission(now time.Time) error {
	if a.Status != HoldStatusOpen {
		if a.Status == HoldStatusClosed {
			return fmt.Errorf("%w: hold is no longer open", ErrAuthorizationClosed)
		}
		return fmt.Errorf("%w: hold status %q is not open", ErrAuthorizationInvalid, a.Status)
	}
	if !a.ExpiresAt.IsZero() && now.After(a.ExpiresAt) {
		return fmt.Errorf("%w: expired at %s", ErrAuthorizationExpired, a.ExpiresAt.UTC().Format(time.RFC3339Nano))
	}
	return nil
}

// AuthorizeInput is the application-side command for one deterministic hold.
type AuthorizeInput struct {
	ID                string
	AccountID         string
	TURKey            string
	MaxCustomerCharge MaxCostBound
	Amount            Money
	PricingRef        VersionRef
	ChargePolicyRef   VersionRef
	ExpiresAt         time.Time
}

// AuthorizationStore is the only persistence capability needed by the billing
// admission use case. Bun and database details stay in infra/billingstore.
type AuthorizationStore interface {
	Authorize(context.Context, AuthorizeInput) (Authorization, error)
}

// HoldReleaser is the unused-hold and stale-safe release seam (Req 17.9).
type HoldReleaser interface {
	ReleaseAuthorization(context.Context, ReleaseAuthorizationInput) (Posting, error)
}

// AuthorizationHoldKey is stable for one account and logical A-leg/TUR. Retry
// labels and wall-clock timestamps are intentionally excluded.
func AuthorizationHoldKey(accountID, turKey string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	turKey = strings.TrimSpace(turKey)
	if accountID == "" || turKey == "" {
		return "", fmt.Errorf("%w: account and TUR identity are required", ErrAuthorizationInvalid)
	}
	// Length-prefix each component so the durable identity remains unambiguous
	// even when either caller-controlled identifier contains ':'.
	return fmt.Sprintf("%d:%s:%d:%s", len(accountID), accountID, len(turKey), turKey), nil
}

func (in AuthorizeInput) normalized() (AuthorizeInput, error) {
	out := in
	out.ID = strings.TrimSpace(out.ID)
	out.AccountID = strings.TrimSpace(out.AccountID)
	out.TURKey = strings.TrimSpace(out.TURKey)
	if out.Amount == (Money{}) {
		out.Amount = in.MaxCustomerCharge.Amount
	}
	if out.ID == "" || out.AccountID == "" || out.TURKey == "" {
		return AuthorizeInput{}, fmt.Errorf("%w: ID, account ID, and TUR key are required", ErrAuthorizationInvalid)
	}
	if err := out.Amount.Validate(); err != nil {
		return AuthorizeInput{}, err
	}
	if out.Amount.Nano < 0 {
		return AuthorizeInput{}, fmt.Errorf("%w: amount cannot be negative", ErrAuthorizationInvalid)
	}
	if out.MaxCustomerCharge.Amount != (Money{}) && out.Amount != out.MaxCustomerCharge.Amount {
		return AuthorizeInput{}, fmt.Errorf("%w: amount differs from max-charge bound", ErrAuthorizationInvalid)
	}
	if out.MaxCustomerCharge.Amount == (Money{}) {
		out.MaxCustomerCharge.Amount = out.Amount
	}
	if out.MaxCustomerCharge.Amount.Currency != out.Amount.Currency {
		return AuthorizeInput{}, ErrMoneyCurrencyMismatch
	}
	if out.PricingRef == (VersionRef{}) {
		out.PricingRef = out.MaxCustomerCharge.PricingRef
	}
	if out.ChargePolicyRef == (VersionRef{}) {
		out.ChargePolicyRef = out.MaxCustomerCharge.ChargePolicyRef
	}
	if out.PricingRef == (VersionRef{}) || out.ChargePolicyRef == (VersionRef{}) {
		return AuthorizeInput{}, fmt.Errorf("%w: pricing and charge policy references are required", ErrAuthorizationInvalid)
	}
	if out.MaxCustomerCharge.PricingRef != (VersionRef{}) && out.MaxCustomerCharge.PricingRef != out.PricingRef {
		return AuthorizeInput{}, fmt.Errorf("%w: pricing reference mismatch", ErrAuthorizationInvalid)
	}
	if out.MaxCustomerCharge.ChargePolicyRef != (VersionRef{}) && out.MaxCustomerCharge.ChargePolicyRef != out.ChargePolicyRef {
		return AuthorizeInput{}, fmt.Errorf("%w: policy reference mismatch", ErrAuthorizationInvalid)
	}
	return out, nil
}

func (in AuthorizeInput) Validate() error {
	_, err := in.normalized()
	return err
}

func (in AuthorizeInput) SemanticFingerprint() (string, error) {
	normalized, err := in.normalized()
	if err != nil {
		return "", err
	}
	canonical := struct {
		ID              string
		AccountID       string
		TURKey          string
		Amount          Money
		PricingRef      VersionRef
		ChargePolicyRef VersionRef
	}{normalized.ID, normalized.AccountID, normalized.TURKey, normalized.Amount, normalized.PricingRef, normalized.ChargePolicyRef}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("authorization:v1:%x", sum[:]), nil
}

func (in AuthorizeInput) Seal() (Authorization, error) {
	normalized, err := in.normalized()
	if err != nil {
		return Authorization{}, err
	}
	fp, err := normalized.SemanticFingerprint()
	if err != nil {
		return Authorization{}, err
	}
	return Authorization{
		ID: normalized.ID, AccountID: normalized.AccountID, TURKey: normalized.TURKey,
		Amount: normalized.Amount, PricingRef: normalized.PricingRef,
		ChargePolicyRef: normalized.ChargePolicyRef, ExpiresAt: normalized.ExpiresAt,
		Fingerprint: fp, Status: HoldStatusOpen,
	}, nil
}

// CheckAuthorizationReplay implements semantic idempotency before mutation.
func CheckAuthorizationReplay(existing Authorization, incoming AuthorizeInput) error {
	sealed, err := incoming.Seal()
	if err != nil {
		return err
	}
	if existing.ID != sealed.ID || existing.AccountID != sealed.AccountID || existing.TURKey != sealed.TURKey {
		return ErrAuthorizationConflict
	}
	if existing.Fingerprint != sealed.Fingerprint {
		return ErrAuthorizationConflict
	}
	return nil
}
