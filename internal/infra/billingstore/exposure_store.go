package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

type exposureRow struct {
	ExposureKey         string     `bun:"exposure_key,pk"`
	AccountID           string     `bun:"account_id,notnull"`
	CallID              string     `bun:"call_id,notnull"`
	MaxExposureNano     int64      `bun:"max_exposure_nano,notnull"`
	Currency            string     `bun:"currency,notnull"`
	PricingRef          string     `bun:"pricing_ref,notnull"`
	ChargePolicyRef     string     `bun:"charge_policy_ref,notnull"`
	Fingerprint         string     `bun:"fingerprint,notnull"`
	BalanceNano         int64      `bun:"balance_nano,notnull"`
	CreditFloorNano     int64      `bun:"credit_floor_nano,notnull"`
	OpenExposureNano    int64      `bun:"open_exposure_nano,notnull"`
	SettledHeadroomNano int64      `bun:"settled_headroom_nano,notnull"`
	SafetyMarginBefore  int64      `bun:"safety_margin_before_nano,notnull"`
	SafetyMarginAfter   int64      `bun:"safety_margin_after_nano,notnull"`
	Status              string     `bun:"status,notnull"`
	CreatedAt           time.Time  `bun:"created_at,notnull"`
	ClosedAt            *time.Time `bun:"closed_at,nullzero"`
}

var _ billing.ExposureAdmissionStore = (*DurableStore)(nil)

func (s *DurableStore) AdmitExposure(ctx context.Context, input billing.AdmitExposureInput) (billing.CallExposure, error) {
	if s == nil || s.db == nil {
		return billing.CallExposure{}, fmt.Errorf("billingstore: nil store")
	}
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	}
	return withAccountTx(ctx, accountTxRetry{Attempts: 40, Delay: 3 * time.Millisecond}, func() (billing.CallExposure, error) {
		return s.admitExposureAttempt(ctx, input)
	})
}

func (s *DurableStore) admitExposureAttempt(ctx context.Context, input billing.AdmitExposureInput) (billing.CallExposure, error) {
	var zero billing.CallExposure
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, fmt.Errorf("billingstore: begin exposure admission: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	accountID := strings.TrimSpace(input.AccountID)
	callID := strings.TrimSpace(input.CallID)
	if accountID == "" || callID == "" {
		return zero, fmt.Errorf("%w: account id and call id are required", billing.ErrExposureInvalid)
	}
	if err := lockAccount(ctx, tx, s.db.Dialect().Name(), accountID); err != nil {
		return zero, err
	}
	var existing exposureRow
	err = tx.NewRaw(`SELECT exposure_key, account_id, call_id, max_exposure_nano, currency, pricing_ref, charge_policy_ref, fingerprint, balance_nano, credit_floor_nano, open_exposure_nano, settled_headroom_nano, safety_margin_before_nano, safety_margin_after_nano, status, created_at, closed_at FROM call_exposures WHERE account_id = ? AND call_id = ?`, accountID, callID).Scan(ctx, &existing)
	if err == nil {
		exposure, decodeErr := exposureFromRow(existing)
		if decodeErr != nil {
			return zero, decodeErr
		}
		if replayErr := billing.CheckExposureReplay(exposure, input); replayErr != nil {
			return zero, replayErr
		}
		if err := tx.Commit(); err != nil {
			return zero, fmt.Errorf("billingstore: commit exposure replay: %w", err)
		}
		return exposure, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, fmt.Errorf("billingstore: lookup exposure: %w", err)
	}
	account, err := getAccountTx(ctx, tx, accountID)
	if err != nil {
		return zero, err
	}
	var rows []exposureRow
	if err := tx.NewRaw(`SELECT exposure_key, account_id, call_id, max_exposure_nano, currency, pricing_ref, charge_policy_ref, fingerprint, balance_nano, credit_floor_nano, open_exposure_nano, settled_headroom_nano, safety_margin_before_nano, safety_margin_after_nano, status, created_at, closed_at FROM call_exposures WHERE account_id = ? AND status = 'open' ORDER BY call_id`, accountID).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return zero, fmt.Errorf("billingstore: list open exposures: %w", err)
	}
	exposures := make([]billing.CallExposure, 0, len(rows))
	for _, row := range rows {
		decoded, decodeErr := exposureFromRow(row)
		if decodeErr != nil {
			return zero, decodeErr
		}
		exposures = append(exposures, decoded)
	}
	admitted, err := billing.EvaluateAdmit(account, exposures, input)
	if err != nil {
		return zero, err
	}
	pricingRef, err := json.Marshal(admitted.PricingRef)
	if err != nil {
		return zero, fmt.Errorf("billingstore: encode exposure pricing ref: %w", err)
	}
	policyRef, err := json.Marshal(admitted.ChargePolicyRef)
	if err != nil {
		return zero, fmt.Errorf("billingstore: encode exposure policy ref: %w", err)
	}
	_, err = tx.NewRaw(`INSERT INTO call_exposures(exposure_key, account_id, call_id, max_exposure_nano, currency, pricing_ref, charge_policy_ref, fingerprint, balance_nano, credit_floor_nano, open_exposure_nano, settled_headroom_nano, safety_margin_before_nano, safety_margin_after_nano, status, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		exposureKey(admitted.AccountID, admitted.CallID), admitted.AccountID, admitted.CallID, admitted.Max.Nano, admitted.Max.Currency, string(pricingRef), string(policyRef), admitted.Fingerprint,
		admitted.Basis.BalanceNano, admitted.Basis.CreditFloorNano, admitted.Basis.OpenExposureNano, admitted.Basis.SettledHeadroomNano, admitted.Basis.SafetyMarginBeforeNano, admitted.Basis.SafetyMarginAfterNano, string(admitted.Status), admitted.CreatedAt).Exec(ctx)
	if err != nil {
		return zero, fmt.Errorf("billingstore: insert exposure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return zero, fmt.Errorf("billingstore: commit exposure: %w", err)
	}
	return admitted, nil
}

func exposureKey(accountID, callID string) string {
	return "call-exposure:v1:" + strings.TrimSpace(accountID) + ":" + strings.TrimSpace(callID)
}

func exposureFromRow(row exposureRow) (billing.CallExposure, error) {
	var pricing, policy billing.VersionRef
	if err := json.Unmarshal([]byte(row.PricingRef), &pricing); err != nil {
		return billing.CallExposure{}, fmt.Errorf("billingstore: decode exposure pricing ref: %w", err)
	}
	if err := json.Unmarshal([]byte(row.ChargePolicyRef), &policy); err != nil {
		return billing.CallExposure{}, fmt.Errorf("billingstore: decode exposure policy ref: %w", err)
	}
	var closedAt time.Time
	if row.ClosedAt != nil {
		closedAt = *row.ClosedAt
	}
	return billing.CallExposure{
		AccountID: row.AccountID, CallID: row.CallID, Max: billing.Money{Nano: row.MaxExposureNano, Currency: row.Currency},
		PricingRef: pricing, ChargePolicyRef: policy, Fingerprint: row.Fingerprint, CreatedAt: row.CreatedAt, ClosedAt: closedAt,
		Status: billing.ExposureStatus(row.Status), Basis: billing.ExposureBasis{
			BalanceNano: row.BalanceNano, CreditFloorNano: row.CreditFloorNano, OpenExposureNano: row.OpenExposureNano,
			SettledHeadroomNano: row.SettledHeadroomNano, SafetyMarginBeforeNano: row.SafetyMarginBefore, SafetyMarginAfterNano: row.SafetyMarginAfter,
		},
	}, nil
}
