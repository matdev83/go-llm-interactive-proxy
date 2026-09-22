package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

var _ billing.CustomerUnitLedger = (*DurableStore)(nil)

type customerUnitBalanceRow struct {
	ID                   int64          `bun:"id,pk"`
	StoreID              string         `bun:"store_id,notnull"`
	IdentityKey          string         `bun:"identity_key,notnull"`
	CanonicalKey         string         `bun:"canonical_key,notnull"`
	AccountID            string         `bun:"account_id,notnull"`
	PoolID               string         `bun:"pool_id,notnull"`
	PeriodID             string         `bun:"period_id,notnull"`
	ComponentKey         string         `bun:"component_key,notnull"`
	Status               string         `bun:"status,notnull"`
	GrantedCoefficient   sql.NullString `bun:"granted_coefficient"`
	GrantedScale         sql.NullInt64  `bun:"granted_scale"`
	AvailableCoefficient sql.NullString `bun:"available_coefficient"`
	AvailableScale       sql.NullInt64  `bun:"available_scale"`
	ReservedCoefficient  sql.NullString `bun:"reserved_coefficient"`
	ReservedScale        sql.NullInt64  `bun:"reserved_scale"`
	ConsumedCoefficient  sql.NullString `bun:"consumed_coefficient"`
	ConsumedScale        sql.NullInt64  `bun:"consumed_scale"`
	Version              uint64         `bun:"version,notnull"`
	Fence                uint64         `bun:"fence,notnull"`
	CreatedAt            time.Time      `bun:"created_at,notnull"`
	UpdatedAt            time.Time      `bun:"updated_at,notnull"`
}

type customerUnitOperationRow struct {
	ID            int64     `bun:"id,pk"`
	StoreID       string    `bun:"store_id,notnull"`
	OperationID   string    `bun:"operation_id,notnull"`
	IdentityKey   string    `bun:"identity_key,notnull"`
	CanonicalKey  string    `bun:"canonical_key,notnull"`
	Version       uint32    `bun:"version,notnull"`
	Kind          string    `bun:"kind,notnull"`
	Source        string    `bun:"source,notnull"`
	ReservationID string    `bun:"reservation_id,notnull"`
	Fingerprint   string    `bun:"fingerprint,notnull"`
	OperationJSON string    `bun:"operation_json,notnull"`
	ResultJSON    string    `bun:"result_json,notnull"`
	CreatedAt     time.Time `bun:"created_at,notnull"`
}

type customerUnitReservationRow struct {
	ID                  int64      `bun:"id,pk"`
	StoreID             string     `bun:"store_id,notnull"`
	ReservationID       string     `bun:"reservation_id,notnull"`
	IdentityKey         string     `bun:"identity_key,notnull"`
	CanonicalKey        string     `bun:"canonical_key,notnull"`
	QuantityCoefficient string     `bun:"quantity_coefficient,notnull"`
	QuantityScale       int        `bun:"quantity_scale,notnull"`
	Status              string     `bun:"status,notnull"`
	SourceOperationID   string     `bun:"source_operation_id,notnull"`
	CreatedAt           time.Time  `bun:"created_at,notnull"`
	ClosedAt            *time.Time `bun:"closed_at,nullzero"`
}

// ApplyCustomerUnitOperation executes one customer-owned unit mutation. The
// operation row, balance transition, and reservation binding all share one
// retryable local transaction. No balance read is exposed to callers.
func (s *DurableStore) ApplyCustomerUnitOperation(ctx context.Context, op billing.CustomerUnitOperation) (billing.CustomerUnitOperationResult, error) {
	if s == nil || s.db == nil {
		return billing.CustomerUnitOperationResult{}, fmt.Errorf("%w: nil store", billing.ErrCustomerUnitUnavailable)
	}
	if ctx == nil {
		return billing.CustomerUnitOperationResult{}, fmt.Errorf("%w: nil context", billing.ErrCustomerUnitInvalid)
	}
	if err := ctx.Err(); err != nil {
		return billing.CustomerUnitOperationResult{}, fmt.Errorf("%w: %w", billing.ErrCustomerUnitUnavailable, err)
	}
	if err := op.Validate(); err != nil {
		return billing.CustomerUnitOperationResult{}, err
	}
	return withAccountTx(ctx, accountTxRetry{Attempts: 80, Delay: 3 * time.Millisecond}, func() (billing.CustomerUnitOperationResult, error) {
		return s.applyCustomerUnitAttempt(ctx, op)
	})
}

func (s *DurableStore) applyCustomerUnitAttempt(ctx context.Context, op billing.CustomerUnitOperation) (billing.CustomerUnitOperationResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.CustomerUnitOperationResult{}, fmt.Errorf("billingstore: begin customer-unit operation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := s.applyCustomerUnitOperationTx(ctx, tx, op)
	if err != nil {
		return billing.CustomerUnitOperationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return billing.CustomerUnitOperationResult{}, fmt.Errorf("billingstore: commit customer-unit operation: %w", err)
	}
	return result, nil
}

// applyCustomerUnitOperationTx applies the customer-unit command to an
// already-open transaction. Settlement uses this narrow helper so a final
// monetary posting and a customer allowance debit commit or roll back together.
func (s *DurableStore) applyCustomerUnitOperationTx(ctx context.Context, tx bun.Tx, op billing.CustomerUnitOperation) (billing.CustomerUnitOperationResult, error) {
	var zero billing.CustomerUnitOperationResult
	key, err := customerUnitStorageKey(op.Key)
	if err != nil {
		return zero, err
	}
	fingerprint, err := op.SemanticFingerprint()
	if err != nil {
		return zero, err
	}
	canonicalKey, err := op.Key.CanonicalKey()
	if err != nil {
		return zero, err
	}
	identityKey, err := op.Key.IdentityKey()
	if err != nil {
		return zero, err
	}

	if existing, found, lookupErr := loadCustomerUnitOperation(ctx, tx, s.db.Dialect().Name(), s.storeID, op.OperationID); lookupErr != nil {
		return zero, lookupErr
	} else if found {
		return finishCustomerUnitReplay(existing, op, fingerprint)
	}

	balanceRow, found, err := loadCustomerUnitBalance(ctx, tx, s.db.Dialect().Name(), s.storeID, identityKey, canonicalKey)
	if err != nil {
		return zero, err
	}
	var balance billing.CustomerUnitBalance
	if found {
		// A SQLite no-op update takes the database writer lock before the
		// transition. PostgreSQL uses SELECT FOR UPDATE in the same helper.
		balanceRow, err = lockAndReloadCustomerUnitBalance(ctx, tx, s.db.Dialect().Name(), balanceRow)
		if err != nil {
			return zero, err
		}
		balance, err = customerUnitBalanceFromRow(balanceRow, op.Key, canonicalKey, identityKey)
		if err != nil {
			return zero, err
		}
		// A concurrent transaction may have inserted the idempotency row while
		// this transaction waited for the balance lock. Recheck before spending.
		if existing, found, lookupErr := loadCustomerUnitOperation(ctx, tx, s.db.Dialect().Name(), s.storeID, op.OperationID); lookupErr != nil {
			return zero, lookupErr
		} else if found {
			return finishCustomerUnitReplay(existing, op, fingerprint)
		}
	}
	if !found {
		balance = billing.CustomerUnitBalance{Key: op.Key, Status: billing.CustomerEntitlementMissing}
		if op.Kind != billing.CustomerUnitOperationGrant {
			_, err := billing.TransitionCustomerUnitBalance(op, balance)
			return zero, err
		}
	}

	var reservation *customerUnitReservationRow
	if op.Kind == billing.CustomerUnitOperationReserve || op.Kind == billing.CustomerUnitOperationCommit || op.Kind == billing.CustomerUnitOperationRelease {
		reservation, err = loadCustomerUnitReservation(ctx, tx, s.db.Dialect().Name(), s.storeID, op.ReservationID)
		if err != nil {
			return zero, err
		}
		switch op.Kind {
		case billing.CustomerUnitOperationReserve:
			if reservation != nil {
				return zero, fmt.Errorf("%w: reservation %q already exists", billing.ErrCustomerUnitReservationInvalid, op.ReservationID)
			}
		case billing.CustomerUnitOperationCommit, billing.CustomerUnitOperationRelease:
			if err := validateOpenCustomerUnitReservation(reservation, op, identityKey, canonicalKey); err != nil {
				return zero, err
			}
			if !decimalEqualText(reservation.QuantityCoefficient, reservation.QuantityScale, op.Quantity) {
				return zero, fmt.Errorf("%w: quantity does not match reservation", billing.ErrCustomerUnitReservationInvalid)
			}
		}
	}

	transition, err := billing.TransitionCustomerUnitBalance(op, balance)
	if err != nil {
		return zero, err
	}
	if err := validateCustomerUnitStoredIdentity(transition.After.Key, identityKey, canonicalKey); err != nil {
		return zero, err
	}

	result, err := makeCustomerUnitResult(op, fingerprint, transition)
	if err != nil {
		return zero, err
	}
	if err := persistCustomerUnitBalance(ctx, tx, s.storeID, identityKey, canonicalKey, key, balance, found, result.After); err != nil {
		return zero, err
	}
	if reservation != nil && (op.Kind == billing.CustomerUnitOperationCommit || op.Kind == billing.CustomerUnitOperationRelease) {
		status := "committed"
		if op.Kind == billing.CustomerUnitOperationRelease {
			status = "released"
		}
		now := time.Now().UTC()
		updated, updateErr := tx.NewRaw(`UPDATE billing_unit_reservations SET status = ?, closed_at = ? WHERE store_id = ? AND reservation_id = ? AND status = 'open'`, status, now, s.storeID, op.ReservationID).Exec(ctx)
		if updateErr != nil {
			return zero, fmt.Errorf("billingstore: close customer-unit reservation: %w", updateErr)
		}
		count, countErr := updated.RowsAffected()
		if countErr != nil {
			return zero, fmt.Errorf("billingstore: close customer-unit reservation: %w", countErr)
		}
		if count != 1 {
			return zero, fmt.Errorf("%w: reservation %q is no longer open", billing.ErrCustomerUnitReservationInvalid, op.ReservationID)
		}
	}
	if op.Kind == billing.CustomerUnitOperationReserve {
		if err := insertCustomerUnitReservation(ctx, tx, s.storeID, identityKey, canonicalKey, op, *result.AppliedQuantity); err != nil {
			return zero, err
		}
	}
	if err := insertCustomerUnitOperation(ctx, tx, s.storeID, identityKey, canonicalKey, op, fingerprint, result); err != nil {
		return zero, err
	}
	return result, nil
}

func finishCustomerUnitReplay(row customerUnitOperationRow, incoming billing.CustomerUnitOperation, fingerprint string) (billing.CustomerUnitOperationResult, error) {
	var zero billing.CustomerUnitOperationResult
	canonicalKey, err := incoming.Key.CanonicalKey()
	if err != nil {
		return zero, err
	}
	identityKey, err := incoming.Key.IdentityKey()
	if err != nil {
		return zero, err
	}
	if row.CanonicalKey != canonicalKey || row.IdentityKey != identityKey {
		return zero, billing.ErrCustomerUnitOperationConflict
	}
	var existing billing.CustomerUnitOperation
	if err := json.Unmarshal([]byte(row.OperationJSON), &existing); err != nil {
		return zero, fmt.Errorf("%w: operation replay payload: %v", billing.ErrCustomerUnitUnavailable, err)
	}
	if err := billing.CheckCustomerUnitOperationReplay(existing, incoming); err != nil {
		return zero, err
	}
	if row.Fingerprint != fingerprint {
		return zero, billing.ErrCustomerUnitOperationConflict
	}
	var result billing.CustomerUnitOperationResult
	if err := json.Unmarshal([]byte(row.ResultJSON), &result); err != nil {
		return zero, fmt.Errorf("%w: operation replay result: %v", billing.ErrCustomerUnitUnavailable, err)
	}
	if err := result.ValidateFor(incoming); err != nil {
		return zero, err
	}
	result.Status = billing.CustomerUnitOperationReplayed
	result.Replayed = true
	if err := result.ValidateFor(incoming); err != nil {
		return zero, err
	}
	return result, nil
}

func loadCustomerUnitOperation(ctx context.Context, tx bun.Tx, dialectName dialect.Name, storeID, operationID string) (customerUnitOperationRow, bool, error) {
	var row customerUnitOperationRow
	query := `SELECT id, store_id, operation_id, identity_key, canonical_key, version, kind, source, reservation_id, fingerprint, operation_json, result_json, created_at FROM billing_unit_operations WHERE store_id = ? AND operation_id = ?`
	if dialectName == dialect.PG {
		query += ` FOR UPDATE`
	}
	err := tx.NewRaw(query, storeID, operationID).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return customerUnitOperationRow{}, false, nil
	}
	if err != nil {
		return customerUnitOperationRow{}, false, fmt.Errorf("billingstore: load customer-unit operation: %w", err)
	}
	return row, true, nil
}

func loadCustomerUnitBalance(ctx context.Context, tx bun.Tx, dialectName dialect.Name, storeID, identityKey, canonicalKey string) (customerUnitBalanceRow, bool, error) {
	var row customerUnitBalanceRow
	query := `SELECT id, store_id, identity_key, canonical_key, account_id, pool_id, period_id, component_key, status, granted_coefficient, granted_scale, available_coefficient, available_scale, reserved_coefficient, reserved_scale, consumed_coefficient, consumed_scale, version, fence, created_at, updated_at FROM billing_unit_balances WHERE store_id = ? AND identity_key = ?`
	if dialectName == dialect.PG {
		query += ` FOR UPDATE`
	}
	err := tx.NewRaw(query, storeID, identityKey).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return customerUnitBalanceRow{}, false, nil
	}
	if err != nil {
		return customerUnitBalanceRow{}, false, fmt.Errorf("billingstore: load customer-unit balance: %w", err)
	}
	if row.CanonicalKey != canonicalKey {
		return customerUnitBalanceRow{}, false, fmt.Errorf("%w: stored canonical key differs from operation", billing.ErrCustomerUnitBalanceInvalid)
	}
	return row, true, nil
}

func lockAndReloadCustomerUnitBalance(ctx context.Context, tx bun.Tx, dialectName dialect.Name, row customerUnitBalanceRow) (customerUnitBalanceRow, error) {
	if dialectName == dialect.SQLite {
		result, err := tx.NewRaw(`UPDATE billing_unit_balances SET updated_at = updated_at WHERE id = ?`, row.ID).Exec(ctx)
		if err != nil {
			return customerUnitBalanceRow{}, fmt.Errorf("billingstore: lock customer-unit balance: %w", err)
		}
		count, countErr := result.RowsAffected()
		if countErr != nil {
			return customerUnitBalanceRow{}, fmt.Errorf("billingstore: lock customer-unit balance: %w", countErr)
		}
		if count != 1 {
			return customerUnitBalanceRow{}, billing.ErrCustomerUnitStaleVersion
		}
	}
	canonical := row.CanonicalKey
	identity := row.IdentityKey
	locked, found, err := loadCustomerUnitBalance(ctx, tx, dialectName, row.StoreID, identity, canonical)
	if err != nil {
		return customerUnitBalanceRow{}, err
	}
	if !found {
		return customerUnitBalanceRow{}, billing.ErrCustomerUnitStaleVersion
	}
	return locked, nil
}

func loadCustomerUnitReservation(ctx context.Context, tx bun.Tx, dialectName dialect.Name, storeID, reservationID string) (*customerUnitReservationRow, error) {
	var row customerUnitReservationRow
	query := `SELECT id, store_id, reservation_id, identity_key, canonical_key, quantity_coefficient, quantity_scale, status, source_operation_id, created_at, closed_at FROM billing_unit_reservations WHERE store_id = ? AND reservation_id = ?`
	if dialectName == dialect.PG {
		query += ` FOR UPDATE`
	}
	err := tx.NewRaw(query, storeID, reservationID).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("billingstore: load customer-unit reservation: %w", err)
	}
	return &row, nil
}

func validateOpenCustomerUnitReservation(row *customerUnitReservationRow, op billing.CustomerUnitOperation, identityKey, canonicalKey string) error {
	if row == nil || row.Status != "open" {
		return fmt.Errorf("%w: reservation is not open", billing.ErrCustomerUnitReservationInvalid)
	}
	if row.IdentityKey != identityKey || row.CanonicalKey != canonicalKey {
		return fmt.Errorf("%w: reservation scope mismatch", billing.ErrCustomerUnitReservationInvalid)
	}
	if row.ReservationID != op.ReservationID {
		return fmt.Errorf("%w: reservation identity mismatch", billing.ErrCustomerUnitReservationInvalid)
	}
	return nil
}

func makeCustomerUnitResult(op billing.CustomerUnitOperation, fingerprint string, transition billing.CustomerUnitTransition) (billing.CustomerUnitOperationResult, error) {
	applied, err := transition.AppliedQuantity.Normalize()
	if err != nil {
		return billing.CustomerUnitOperationResult{}, err
	}
	uncovered, err := transition.Decision.Uncovered.Normalize()
	if err != nil {
		return billing.CustomerUnitOperationResult{}, err
	}
	var fallback *billing.Money
	if transition.Decision.MonetaryFallbackBound != nil {
		bound := *transition.Decision.MonetaryFallbackBound
		fallback = &bound
	}
	result := billing.CustomerUnitOperationResult{
		OperationID: op.OperationID, Key: op.Key, Kind: op.Kind,
		Status: billing.CustomerUnitOperationApplied, Entitlement: transition.After.Status,
		Before: transition.Before, After: transition.After,
		AppliedQuantity: &applied, UncoveredQuantity: &uncovered,
		FallbackRequired: transition.Decision.FallbackRequired, FallbackBound: fallback,
		Fingerprint: fingerprint, ReservationID: op.ReservationID,
	}
	if err := result.ValidateFor(op); err != nil {
		return billing.CustomerUnitOperationResult{}, err
	}
	return result, nil
}

func persistCustomerUnitBalance(ctx context.Context, tx bun.Tx, storeID, identityKey, canonicalKey string, key customerUnitStorageParts, before billing.CustomerUnitBalance, found bool, after billing.CustomerUnitBalance) error {
	if err := after.Validate(); err != nil {
		return err
	}
	values, err := customerUnitBalanceValues(after)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if !found {
		_, err := tx.NewRaw(`INSERT INTO billing_unit_balances(store_id, identity_key, canonical_key, account_id, pool_id, period_id, component_key, status, granted_coefficient, granted_scale, available_coefficient, available_scale, reserved_coefficient, reserved_scale, consumed_coefficient, consumed_scale, version, fence, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, storeID, identityKey, canonicalKey, key.AccountID, key.PoolID, key.PeriodID, key.ComponentKey, string(after.Status), values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7], after.Version, after.Fence, now, now).Exec(ctx)
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("billingstore: customer-unit balance insert race: %w", err)
			}
			return fmt.Errorf("billingstore: insert customer-unit balance: %w", err)
		}
		return nil
	}
	// ExpectedVersion and Fence are enforced by the same conditional mutation
	// as the state write. The domain transition has already checked them against
	// before, while this predicate closes a concurrent lost-update window.
	result, err := tx.NewRaw(`UPDATE billing_unit_balances SET status = ?, granted_coefficient = ?, granted_scale = ?, available_coefficient = ?, available_scale = ?, reserved_coefficient = ?, reserved_scale = ?, consumed_coefficient = ?, consumed_scale = ?, version = ?, fence = ?, updated_at = ? WHERE store_id = ? AND identity_key = ? AND canonical_key = ? AND version = ? AND fence <= ?`, string(after.Status), values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7], after.Version, after.Fence, now, storeID, identityKey, canonicalKey, before.Version, after.Fence).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: update customer-unit balance: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("billingstore: update customer-unit balance: %w", err)
	}
	if count != 1 {
		return billing.ErrCustomerUnitStaleVersion
	}
	return nil
}

type customerUnitStorageParts struct {
	AccountID    string
	PoolID       string
	PeriodID     string
	ComponentKey string
}

func customerUnitStorageKey(key billing.CustomerUnitKey) (customerUnitStorageParts, error) {
	componentJSON, err := key.Component.CanonicalJSON()
	if err != nil {
		return customerUnitStorageParts{}, err
	}
	if err := key.Validate(); err != nil {
		return customerUnitStorageParts{}, err
	}
	return customerUnitStorageParts{AccountID: key.AccountID, PoolID: key.PoolID, PeriodID: key.PeriodID, ComponentKey: string(componentJSON)}, nil
}

func validateCustomerUnitStoredIdentity(key billing.CustomerUnitKey, identityKey, canonicalKey string) error {
	storedCanonical, err := key.CanonicalKey()
	if err != nil {
		return err
	}
	if storedCanonical != canonicalKey {
		return fmt.Errorf("%w: balance canonical key changed", billing.ErrCustomerUnitBalanceInvalid)
	}
	storedIdentity, err := key.IdentityKey()
	if err != nil {
		return err
	}
	if storedIdentity != identityKey {
		return fmt.Errorf("%w: balance identity digest changed", billing.ErrCustomerUnitBalanceInvalid)
	}
	return nil
}

func customerUnitBalanceValues(balance billing.CustomerUnitBalance) ([8]any, error) {
	values := [8]any{}
	decimals := []*metering.Decimal{balance.Granted, balance.Available, balance.Reserved, balance.Consumed}
	for i, value := range decimals {
		if value == nil {
			values[i*2], values[i*2+1] = nil, nil
			continue
		}
		normalized, err := value.Normalize()
		if err != nil {
			return [8]any{}, err
		}
		values[i*2], values[i*2+1] = normalized.Coefficient, int(normalized.Scale)
	}
	return values, nil
}

func insertCustomerUnitReservation(ctx context.Context, tx bun.Tx, storeID, identityKey, canonicalKey string, op billing.CustomerUnitOperation, quantity metering.Decimal) error {
	normalized, err := quantity.Normalize()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = tx.NewRaw(`INSERT INTO billing_unit_reservations(store_id, reservation_id, identity_key, canonical_key, quantity_coefficient, quantity_scale, status, source_operation_id, created_at) VALUES (?,?,?,?,?,?,?,?,?)`, storeID, op.ReservationID, identityKey, canonicalKey, normalized.Coefficient, int(normalized.Scale), "open", op.OperationID, now).Exec(ctx)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("billingstore: customer-unit reservation race: %w", err)
		}
		return fmt.Errorf("billingstore: insert customer-unit reservation: %w", err)
	}
	return nil
}

func insertCustomerUnitOperation(ctx context.Context, tx bun.Tx, storeID, identityKey, canonicalKey string, op billing.CustomerUnitOperation, fingerprint string, result billing.CustomerUnitOperationResult) error {
	operationJSON, err := json.Marshal(op)
	if err != nil {
		return fmt.Errorf("billingstore: encode customer-unit operation: %w", err)
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("billingstore: encode customer-unit result: %w", err)
	}
	var fallbackNano any
	var fallbackCurrency string
	if op.MonetaryFallbackBound != nil {
		fallbackNano = op.MonetaryFallbackBound.Nano
		fallbackCurrency = op.MonetaryFallbackBound.Currency
	}
	quantity, err := op.Quantity.Normalize()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = tx.NewRaw(`INSERT INTO billing_unit_operations(store_id, operation_id, identity_key, canonical_key, version, kind, source, reservation_id, quantity_coefficient, quantity_scale, expected_version, fence, fallback_nano, fallback_currency, fingerprint, operation_json, result_json, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, storeID, op.OperationID, identityKey, canonicalKey, op.Version, string(op.Kind), string(op.Source), op.ReservationID, quantity.Coefficient, int(quantity.Scale), op.ExpectedVersion, op.Fence, fallbackNano, fallbackCurrency, fingerprint, string(operationJSON), string(resultJSON), now).Exec(ctx)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("billingstore: customer-unit operation race: %w", err)
		}
		return fmt.Errorf("billingstore: insert customer-unit operation: %w", err)
	}
	return nil
}

func decimalEqualText(coefficient string, scale int, incoming metering.Decimal) bool {
	stored := metering.Decimal{Coefficient: coefficient}
	if scale < 0 || scale > 255 {
		return false
	}
	stored.Scale = uint8(scale)
	normalized, err := incoming.Normalize()
	return err == nil && stored.Equal(normalized)
}

// CustomerUnitBalance reads the current authoritative unit balance for one
// customer-owned entitlement key. It validates store, context, and key scope
// and propagates every storage error: callers must fail closed rather than
// treat an outage or a missing balance as zero. Like the former internal
// diagnostic helper, it is intentionally not part of CustomerUnitLedger, so
// runtime callers cannot turn it into read-then-spend.
func (s *DurableStore) CustomerUnitBalance(ctx context.Context, key billing.CustomerUnitKey) (billing.CustomerUnitBalance, error) {
	if s == nil || s.db == nil {
		return billing.CustomerUnitBalance{}, fmt.Errorf("%w: nil store", billing.ErrCustomerUnitUnavailable)
	}
	canonical, err := key.CanonicalKey()
	if err != nil {
		return billing.CustomerUnitBalance{}, err
	}
	identity, err := key.IdentityKey()
	if err != nil {
		return billing.CustomerUnitBalance{}, err
	}
	var row customerUnitBalanceRow
	err = s.db.NewRaw(`SELECT id, store_id, identity_key, canonical_key, account_id, pool_id, period_id, component_key, status, granted_coefficient, granted_scale, available_coefficient, available_scale, reserved_coefficient, reserved_scale, consumed_coefficient, consumed_scale, version, fence, created_at, updated_at FROM billing_unit_balances WHERE store_id = ? AND identity_key = ?`, s.storeID, identity).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return billing.CustomerUnitBalance{Key: key, Status: billing.CustomerEntitlementMissing}, nil
	}
	if err != nil {
		return billing.CustomerUnitBalance{}, err
	}
	return customerUnitBalanceFromRow(row, key, canonical, identity)
}

func customerUnitBalanceFromRow(row customerUnitBalanceRow, requestedKey billing.CustomerUnitKey, canonicalKey, identityKey string) (billing.CustomerUnitBalance, error) {
	if row.CanonicalKey != canonicalKey || row.IdentityKey != identityKey {
		return billing.CustomerUnitBalance{}, fmt.Errorf("%w: stored customer-unit identity mismatch", billing.ErrCustomerUnitBalanceInvalid)
	}
	var component metering.ComponentKey
	if err := json.Unmarshal([]byte(row.ComponentKey), &component); err != nil {
		return billing.CustomerUnitBalance{}, fmt.Errorf("%w: stored component key: %v", billing.ErrCustomerUnitBalanceInvalid, err)
	}
	storedKey := billing.CustomerUnitKey{AccountID: row.AccountID, PoolID: row.PoolID, PeriodID: row.PeriodID, Component: component}
	if !storedKey.Equal(requestedKey) {
		return billing.CustomerUnitBalance{}, fmt.Errorf("%w: stored customer-unit key mismatch", billing.ErrCustomerUnitBalanceInvalid)
	}
	granted, err := customerUnitNullableDecimal(row.GrantedCoefficient, row.GrantedScale)
	if err != nil {
		return billing.CustomerUnitBalance{}, err
	}
	available, err := customerUnitNullableDecimal(row.AvailableCoefficient, row.AvailableScale)
	if err != nil {
		return billing.CustomerUnitBalance{}, err
	}
	reserved, err := customerUnitNullableDecimal(row.ReservedCoefficient, row.ReservedScale)
	if err != nil {
		return billing.CustomerUnitBalance{}, err
	}
	consumed, err := customerUnitNullableDecimal(row.ConsumedCoefficient, row.ConsumedScale)
	if err != nil {
		return billing.CustomerUnitBalance{}, err
	}
	balance := billing.CustomerUnitBalance{Key: storedKey, Status: billing.CustomerEntitlementStatus(row.Status), Granted: granted, Available: available, Reserved: reserved, Consumed: consumed, Version: row.Version, Fence: row.Fence}
	if err := balance.Validate(); err != nil {
		return billing.CustomerUnitBalance{}, err
	}
	return balance, nil
}

func customerUnitNullableDecimal(coefficient sql.NullString, scale sql.NullInt64) (*metering.Decimal, error) {
	if !coefficient.Valid && !scale.Valid {
		return nil, nil
	}
	if !coefficient.Valid || !scale.Valid || scale.Int64 < 0 || scale.Int64 > int64(metering.MaxDecimalScale) {
		return nil, fmt.Errorf("%w: invalid stored decimal", billing.ErrCustomerUnitBalanceInvalid)
	}
	value := metering.Decimal{Coefficient: coefficient.String, Scale: uint8(scale.Int64)}
	if err := value.Validate(); err != nil {
		return nil, fmt.Errorf("%w: stored decimal: %v", billing.ErrCustomerUnitBalanceInvalid, err)
	}
	return &value, nil
}
