package billing

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrExposureInvalid          = errors.New("billing: invalid call exposure")
	ErrExposureInsufficient     = errors.New("billing: insufficient safety margin")
	ErrExposureConflict         = errors.New("billing: call exposure replay conflict")
	ErrExposureNotFound         = errors.New("billing: call exposure not found")
	ErrExposureClosed           = errors.New("billing: call exposure is closed")
	ErrExposureActualExceedsMax = errors.New("billing: actual charge exceeds admitted maximum")
)

type ExposureReconciliationReport struct {
	AccountID string
	Currency  string
	Open      Money
	Rows      int
	OK        bool
	Issues    []ReconciliationIssue
}
type ExposureStatus string

const (
	ExposureOpen   ExposureStatus = "open"
	ExposureClosed ExposureStatus = "closed"
)

type ExposureBasis struct {
	BalanceNano            int64
	CreditFloorNano        int64
	OpenExposureNano       int64
	SettledHeadroomNano    int64
	SafetyMarginBeforeNano int64
	SafetyMarginAfterNano  int64
}
type CallExposure struct {
	AccountID       string
	CallID          string
	Max             Money
	PricingRef      VersionRef
	ChargePolicyRef VersionRef
	Fingerprint     string
	CreatedAt       time.Time
	ClosedAt        time.Time
	Status          ExposureStatus
	Basis           ExposureBasis
}

func (e CallExposure) IsOpen() bool {
	return e.Status == ExposureOpen && e.ClosedAt.IsZero()
}

type AdmitExposureInput struct {
	AccountID       string
	CallID          string
	Max             Money
	PricingRef      VersionRef
	ChargePolicyRef VersionRef
	Now             time.Time
}

func (in AdmitExposureInput) normalized() (AdmitExposureInput, error) {
	out := in
	out.AccountID = strings.TrimSpace(out.AccountID)
	out.CallID = strings.TrimSpace(out.CallID)
	if out.AccountID == "" || out.CallID == "" {
		return AdmitExposureInput{}, fmt.Errorf("%w: account id and call id are required", ErrExposureInvalid)
	}
	if err := out.Max.Validate(); err != nil {
		return AdmitExposureInput{}, err
	}
	if out.Max.Nano < 0 {
		return AdmitExposureInput{}, fmt.Errorf("%w: max exposure cannot be negative", ErrExposureInvalid)
	}
	if out.PricingRef == (VersionRef{}) || out.ChargePolicyRef == (VersionRef{}) {
		return AdmitExposureInput{}, fmt.Errorf("%w: pricing and charge policy references are required", ErrExposureInvalid)
	}
	return out, nil
}

func (in AdmitExposureInput) SemanticFingerprint() (string, error) {
	normalized, err := in.normalized()
	if err != nil {
		return "", err
	}
	canonical := struct {
		AccountID       string
		CallID          string
		Max             Money
		PricingRef      VersionRef
		ChargePolicyRef VersionRef
	}{
		AccountID: normalized.AccountID, CallID: normalized.CallID, Max: normalized.Max,
		PricingRef: normalized.PricingRef, ChargePolicyRef: normalized.ChargePolicyRef,
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("exposure:v1:%x", sum[:]), nil
}

func SettledHeadroom(account Account) (Money, error) {
	if err := account.Validate(); err != nil {
		return Money{}, err
	}
	nano, err := checkedSub(account.BalanceNano, account.CreditFloorNano())
	if err != nil {
		return Money{}, err
	}
	return Money{Nano: nano, Currency: account.Currency}, nil
}

func OpenExposure(currency string, exposures []CallExposure) (Money, error) {
	currency = strings.TrimSpace(currency)
	if currency == "" {
		return Money{}, fmt.Errorf("%w: currency is required", ErrMoneyInvalid)
	}
	seen := make(map[string]struct{}, len(exposures))
	var sum int64
	for _, exposure := range exposures {
		if !exposure.IsOpen() {
			continue
		}
		if strings.TrimSpace(exposure.CallID) == "" {
			return Money{}, fmt.Errorf("%w: open exposure call id is required", ErrExposureInvalid)
		}
		if _, exists := seen[exposure.CallID]; exists {
			return Money{}, fmt.Errorf("%w: duplicate open call id %q", ErrExposureInvalid, exposure.CallID)
		}
		seen[exposure.CallID] = struct{}{}
		if err := exposure.Max.Validate(); err != nil {
			return Money{}, err
		}
		if exposure.Max.Currency != currency {
			return Money{}, fmt.Errorf("%w: %q versus %q", ErrMoneyCurrencyMismatch, exposure.Max.Currency, currency)
		}
		if exposure.Max.Nano < 0 {
			return Money{}, fmt.Errorf("%w: open exposure amount cannot be negative", ErrExposureInvalid)
		}
		var err error
		sum, err = checkedAdd(sum, exposure.Max.Nano)
		if err != nil {
			return Money{}, err
		}
	}
	return Money{Nano: sum, Currency: currency}, nil
}

func SafetyMargin(account Account, exposures []CallExposure) (Money, error) {
	headroom, err := SettledHeadroom(account)
	if err != nil {
		return Money{}, err
	}
	open, err := OpenExposure(account.Currency, exposures)
	if err != nil {
		return Money{}, err
	}
	return headroom.Sub(open)
}

func CheckExposureReplay(existing CallExposure, incoming AdmitExposureInput) error {
	normalized, err := incoming.normalized()
	if err != nil {
		return err
	}
	if existing.AccountID != normalized.AccountID || existing.CallID != normalized.CallID {
		return ErrExposureConflict
	}
	fingerprint, err := incoming.SemanticFingerprint()
	if err != nil {
		return err
	}
	if existing.Fingerprint != fingerprint {
		return ErrExposureConflict
	}
	return nil
}

func EvaluateAdmit(account Account, exposures []CallExposure, in AdmitExposureInput) (CallExposure, error) {
	if err := account.Validate(); err != nil {
		return CallExposure{}, err
	}
	if account.State != AccountReady {
		return CallExposure{}, ErrAccountNotReady
	}
	normalized, err := in.normalized()
	if err != nil {
		return CallExposure{}, err
	}
	if normalized.AccountID != account.ID {
		return CallExposure{}, fmt.Errorf("%w: account id mismatch", ErrExposureInvalid)
	}
	if normalized.Max.Currency != account.Currency {
		return CallExposure{}, ErrMoneyCurrencyMismatch
	}
	fingerprint, err := normalized.SemanticFingerprint()
	if err != nil {
		return CallExposure{}, err
	}
	for _, existing := range exposures {
		if existing.CallID != normalized.CallID {
			continue
		}
		if err := CheckExposureReplay(existing, normalized); err != nil {
			return CallExposure{}, err
		}
		return existing, nil
	}
	margin, err := SafetyMargin(account, exposures)
	if err != nil {
		return CallExposure{}, err
	}
	if margin.Nano < normalized.Max.Nano {
		return CallExposure{}, fmt.Errorf("%w: safety margin %d below new max %d", ErrExposureInsufficient, margin.Nano, normalized.Max.Nano)
	}
	after, err := checkedSub(margin.Nano, normalized.Max.Nano)
	if err != nil {
		return CallExposure{}, err
	}
	headroom, err := SettledHeadroom(account)
	if err != nil {
		return CallExposure{}, err
	}
	open, err := OpenExposure(account.Currency, exposures)
	if err != nil {
		return CallExposure{}, err
	}
	return CallExposure{
		AccountID: normalized.AccountID, CallID: normalized.CallID,
		Max: normalized.Max, PricingRef: normalized.PricingRef,
		ChargePolicyRef: normalized.ChargePolicyRef, Fingerprint: fingerprint,
		CreatedAt: normalized.Now, Status: ExposureOpen,
		Basis: ExposureBasis{
			BalanceNano:            account.BalanceNano,
			CreditFloorNano:        account.CreditFloorNano(),
			OpenExposureNano:       open.Nano,
			SettledHeadroomNano:    headroom.Nano,
			SafetyMarginBeforeNano: margin.Nano,
			SafetyMarginAfterNano:  after,
		},
	}, nil
}

type SettleExposureInput struct {
	CallID string
	Actual Money
	Now    time.Time
}
type SettleExposureResult struct {
	Account            Account
	Exposure           CallExposure
	SafetyMarginBefore Money
	SafetyMarginAfter  Money
}

func EvaluateSettle(account Account, exposures []CallExposure, in SettleExposureInput) (SettleExposureResult, error) {
	if err := account.Validate(); err != nil {
		return SettleExposureResult{}, err
	}
	in.CallID = strings.TrimSpace(in.CallID)
	if in.CallID == "" {
		return SettleExposureResult{}, fmt.Errorf("%w: call id is required", ErrExposureInvalid)
	}
	if err := in.Actual.Validate(); err != nil {
		return SettleExposureResult{}, err
	}
	if in.Actual.Nano < 0 {
		return SettleExposureResult{}, fmt.Errorf("%w: actual charge cannot be negative", ErrExposureInvalid)
	}
	if in.Actual.Currency != account.Currency {
		return SettleExposureResult{}, ErrMoneyCurrencyMismatch
	}
	idx := -1
	for i := range exposures {
		if exposures[i].CallID == in.CallID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return SettleExposureResult{}, fmt.Errorf("%w: call %q", ErrExposureNotFound, in.CallID)
	}
	found := exposures[idx]
	if !found.IsOpen() {
		return SettleExposureResult{}, fmt.Errorf("%w: call %q", ErrExposureClosed, in.CallID)
	}
	if found.Max.Currency != in.Actual.Currency {
		return SettleExposureResult{}, ErrMoneyCurrencyMismatch
	}
	if in.Actual.Nano > found.Max.Nano {
		return SettleExposureResult{}, ErrExposureActualExceedsMax
	}
	before, err := SafetyMargin(account, exposures)
	if err != nil {
		return SettleExposureResult{}, err
	}
	updated, err := account.ApplyBalanceDelta(Money{Nano: -in.Actual.Nano, Currency: account.Currency})
	if err != nil {
		return SettleExposureResult{}, err
	}
	closed := found
	closed.Status = ExposureClosed
	closed.ClosedAt = in.Now
	remaining := make([]CallExposure, len(exposures))
	copy(remaining, exposures)
	remaining[idx] = closed
	after, err := SafetyMargin(updated, remaining)
	if err != nil {
		return SettleExposureResult{}, err
	}
	if after.Nano < before.Nano {
		return SettleExposureResult{}, fmt.Errorf("%w: safety margin decreased from %d to %d", ErrExposureInvalid, before.Nano, after.Nano)
	}
	return SettleExposureResult{
		Account: updated, Exposure: closed,
		SafetyMarginBefore: before, SafetyMarginAfter: after,
	}, nil
}
