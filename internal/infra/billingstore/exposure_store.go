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
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

type exposureRow struct {
	ExposureKey         string     `bun:"exposure_key,pk"`
	AccountID           string     `bun:"account_id,notnull"`
	CallID              string     `bun:"call_id,notnull"`
	MaxExposureNano     int64      `bun:"max_exposure_nano,notnull"`
	Currency            string     `bun:"currency,notnull"`
	PricingRef          string     `bun:"pricing_ref,notnull"`
	ChargePolicyRef     string     `bun:"charge_policy_ref,notnull"`
	RouteTariffs        string     `bun:"route_tariffs,notnull"`
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
	// F1: lock the per-store marker before account/exposure effects so
	// activation serializes. Missing markers initialize inside the same tx.
	// F2A: capture the locked snapshot so admission-time V1 ownership pinning
	// uses the canonical customer settlement identity and current marker epoch
	// in the SAME transaction as exposure admission (before stream execution).
	admissionMarker, err := s.ensureAndLockAccountingCutoverTx(ctx, tx)
	if err != nil {
		return zero, err
	}
	if err := lockAccount(ctx, tx, s.db.Dialect().Name(), accountID); err != nil {
		return zero, err
	}
	var existing exposureRow
	err = tx.NewRaw(`SELECT exposure_key, account_id, call_id, max_exposure_nano, currency, pricing_ref, charge_policy_ref, route_tariffs, fingerprint, balance_nano, credit_floor_nano, open_exposure_nano, settled_headroom_nano, safety_margin_before_nano, safety_margin_after_nano, status, created_at, closed_at FROM call_exposures WHERE account_id = ? AND call_id = ?`, accountID, callID).Scan(ctx, &existing)
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
	// B2a new-work gate: ordinary V1 exposure admissions are fenced in
	// draining/active. Exact replay above remains allowed per B1. Uses the
	// tx-scoped gate so concurrent admissions do not deadlock (C-6).
	if err := s.cutoverGateForNewV1Tx(ctx, tx); err != nil {
		_ = tx.Rollback()
		return zero, err
	}
	account, err := getAccountTx(ctx, tx, accountID)
	if err != nil {
		return zero, err
	}
	var rows []exposureRow
	if err := tx.NewRaw(`SELECT exposure_key, account_id, call_id, max_exposure_nano, currency, pricing_ref, charge_policy_ref, route_tariffs, fingerprint, balance_nano, credit_floor_nano, open_exposure_nano, settled_headroom_nano, safety_margin_before_nano, safety_margin_after_nano, status, created_at, closed_at FROM call_exposures WHERE account_id = ? AND status = 'open' ORDER BY call_id`, accountID).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
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
	routeTariffs, err := json.Marshal(admitted.RouteTariffs)
	if err != nil {
		return zero, fmt.Errorf("billingstore: encode exposure route tariffs: %w", err)
	}
	_, err = tx.NewRaw(`INSERT INTO call_exposures(exposure_key, account_id, call_id, max_exposure_nano, currency, pricing_ref, charge_policy_ref, route_tariffs, fingerprint, balance_nano, credit_floor_nano, open_exposure_nano, settled_headroom_nano, safety_margin_before_nano, safety_margin_after_nano, status, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		exposureKey(admitted.AccountID, admitted.CallID), admitted.AccountID, admitted.CallID, admitted.Max.Nano, admitted.Max.Currency, string(pricingRef), string(policyRef), string(routeTariffs), admitted.Fingerprint,
		admitted.Basis.BalanceNano, admitted.Basis.CreditFloorNano, admitted.Basis.OpenExposureNano, admitted.Basis.SettledHeadroomNano, admitted.Basis.SafetyMarginBeforeNano, admitted.Basis.SafetyMarginAfterNano, string(admitted.Status), admitted.CreatedAt).Exec(ctx)
	if err != nil {
		return zero, fmt.Errorf("billingstore: insert exposure: %w", err)
	}
	// F2A: durably pin V1 ownership at legitimate admission in the same tx,
	// using the canonical customer settlement identity and current marker
	// epoch. Genuinely new calls after drain are already fenced above; this
	// pin proves pre-boundary admission for terminal handoff. Best-effort
	// when the call identity cannot form a canonical pin key (legacy
	// unclassifiable exposures remain for drain-time classification to report
	// explicitly); never invents completion.
	if err := s.insertAdmissionCustomerPinTx(ctx, tx, admissionMarker, admitted.AccountID, admitted.CallID); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, fmt.Errorf("billingstore: commit exposure: %w", err)
	}
	return admitted, nil
}

func exposureKey(accountID, callID string) string {
	return "call-exposure:v1:" + strings.TrimSpace(accountID) + ":" + strings.TrimSpace(callID)
}

// insertAdmissionCustomerPinTx durably pins V1 ownership at legitimate call
// admission in the same tx as exposure insertion, using the canonical customer
// settlement identity and current marker epoch. The caller already holds the
// per-store marker lock and has passed the new-V1 gate, so this pin proves
// pre-boundary admission for terminal handoff. Scope-less migration stores
// (empty storeID) bypass cutover scope and skip pinning. Unparseable call
// identities skip pinning (legacy unclassifiable exposures remain for
// drain-time classification to report explicitly). Existing V1 pins are
// idempotent; conflicting V2 ownership fails closed.
func (s *DurableStore) insertAdmissionCustomerPinTx(ctx context.Context, tx bun.Tx, marker billing.AccountingCutoverMarker, accountID, callIDStr string) error {
	if strings.TrimSpace(s.storeID) == "" {
		return nil
	}
	accountID = strings.TrimSpace(accountID)
	callIDStr = strings.TrimSpace(callIDStr)
	if accountID == "" || callIDStr == "" {
		return nil
	}
	callID, err := billing.ParseBillingCallID(callIDStr)
	if err != nil {
		return nil
	}
	opKey, err := billing.CustomerPostingOperationKey(accountID, callID)
	if err != nil {
		return nil
	}
	if existingRow, found, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, opKey); err != nil {
		return err
	} else if found {
		existing, err := postingOwnershipRowToPin(existingRow)
		if err != nil {
			return err
		}
		if existing.Owner != billing.PostingOwnerV1 {
			return fmt.Errorf("%w: customer pin for %q owned by %q, admission requires V1",
				billing.ErrPostingOwnershipConflict, opKey, existing.Owner)
		}
		return nil
	}
	now := time.Now().UTC().UnixNano()
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationCustomerSettlement), opKey, accountID, callID.String(), "", "", "", "", "", billing.PostingOwnerV1, int64(marker.Version), int64(marker.Epoch), marker.Generation, string(marker.State), string(billing.PostingPinPinned), "", "", now, now, 0).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: insert admission customer pin: %w", err)
	}
	row, found, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: admission customer pin unavailable after insert", billing.ErrPostingOwnershipNotFound)
	}
	pin, err := postingOwnershipRowToPin(row)
	if err != nil {
		return err
	}
	if pin.Owner != billing.PostingOwnerV1 {
		return fmt.Errorf("%w: customer pin for %q owned by %q, admission requires V1",
			billing.ErrPostingOwnershipConflict, opKey, pin.Owner)
	}
	return nil
}

func exposureFromRow(row exposureRow) (billing.CallExposure, error) {
	var pricing, policy billing.VersionRef
	if err := json.Unmarshal([]byte(row.PricingRef), &pricing); err != nil {
		return billing.CallExposure{}, fmt.Errorf("billingstore: decode exposure pricing ref: %w", err)
	}
	if err := json.Unmarshal([]byte(row.ChargePolicyRef), &policy); err != nil {
		return billing.CallExposure{}, fmt.Errorf("billingstore: decode exposure policy ref: %w", err)
	}
	var routeTariffs []billing.RouteTariffBinding
	if trimmed := strings.TrimSpace(row.RouteTariffs); trimmed != "" && trimmed != "[]" {
		if err := json.Unmarshal([]byte(row.RouteTariffs), &routeTariffs); err != nil {
			return billing.CallExposure{}, fmt.Errorf("billingstore: decode exposure route tariffs: %w", err)
		}
	}
	var closedAt time.Time
	if row.ClosedAt != nil {
		closedAt = *row.ClosedAt
	}
	return billing.CallExposure{
		AccountID: row.AccountID, CallID: row.CallID, Max: billing.Money{Nano: row.MaxExposureNano, Currency: row.Currency},
		PricingRef: pricing, ChargePolicyRef: policy, RouteTariffs: routeTariffs,
		Fingerprint: row.Fingerprint, CreatedAt: row.CreatedAt, ClosedAt: closedAt,
		Status: billing.ExposureStatus(row.Status), Basis: billing.ExposureBasis{
			BalanceNano: row.BalanceNano, CreditFloorNano: row.CreditFloorNano, OpenExposureNano: row.OpenExposureNano,
			SettledHeadroomNano: row.SettledHeadroomNano, SafetyMarginBeforeNano: row.SafetyMarginBefore, SafetyMarginAfterNano: row.SafetyMarginAfter,
		},
	}, nil
}
