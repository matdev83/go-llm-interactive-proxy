package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

type allocationRow struct {
	ID                int64  `bun:"id"`
	AllocationID      string `bun:"allocation_id"`
	AllocationVersion int64  `bun:"allocation_version"`
	CanonicalJSON     string `bun:"canonical_json"`
	Fingerprint       string `bun:"fingerprint"`
}

type allocationTargetProjection struct {
	StoreID           string `bun:"store_id"`
	AllocationID      string `bun:"allocation_id"`
	AllocationVersion int64  `bun:"allocation_version"`
	TargetID          string `bun:"target_id"`
	TargetKind        string `bun:"target_kind"`
	TargetSubjectID   string `bun:"target_subject_id"`
	TargetJSON        string `bun:"target_json"`
	TargetTenantID    string `bun:"target_tenant_id"`
	TargetPoolID      string `bun:"target_pool_id"`
	TargetWindowID    string `bun:"target_window_id"`
	TargetResetAt     int64  `bun:"target_reset_at_unix"`
	TargetStartAt     int64  `bun:"target_start_at_unix"`
	TargetEndAt       int64  `bun:"target_end_at_unix"`
	AccountID         string `bun:"account_id"`
	PeriodID          string `bun:"period_id"`
}

// AppendAllocation appends one canonical immutable allocation and all of its
// target projections atomically. Replaying the same identity is idempotent;
// changing its canonical payload is an identity conflict.
func (s *DurableStore) AppendAllocation(ctx context.Context, record economics.AllocationRecord) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	const (
		allocationTxAttempts = 40
		allocationTxDelay    = 3 * time.Millisecond
	)
	var lastErr error
	for attempt := 0; attempt < allocationTxAttempts; attempt++ {
		if err := s.appendAllocationOnce(ctx, record); err == nil {
			return nil
		} else {
			lastErr = err
			if !isAllocationSQLiteContention(s, err) || attempt == allocationTxAttempts-1 {
				return err
			}
		}
		if err := waitContention(ctx, time.Duration(attempt+1)*allocationTxDelay); err != nil {
			return err
		}
	}
	return lastErr
}

func (s *DurableStore) appendAllocationOnce(ctx context.Context, record economics.AllocationRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: allocation begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.AppendAllocationInTx(ctx, tx, record); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: allocation commit: %w", err)
	}
	return nil
}

func canonicalAllocationForStore(storeID string, record economics.AllocationRecord) (economics.AllocationRecord, []byte, error) {
	if record.SourceSubject.StoreID != storeID {
		return economics.AllocationRecord{}, nil, fmt.Errorf("%w: allocation source subject store", ErrEconomicsOutOfScope)
	}
	if record.Version > math.MaxInt64 || record.Revision > math.MaxInt64 {
		return economics.AllocationRecord{}, nil, fmt.Errorf("billingstore: allocation version exceeds database range")
	}
	canonical, err := record.Canonical()
	if err != nil {
		return economics.AllocationRecord{}, nil, err
	}
	payload, err := canonical.CanonicalJSON()
	if err != nil {
		return economics.AllocationRecord{}, nil, err
	}
	return canonical, payload, nil
}

func (s *DurableStore) AppendAllocationInTx(ctx context.Context, tx bun.Tx, record economics.AllocationRecord) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	if err := s.lockAllocationStore(ctx, tx); err != nil {
		return err
	}
	canonical, payload, err := canonicalAllocationForStore(s.storeID, record)
	if err != nil {
		return err
	}
	fingerprint := canonical.Fingerprint()
	var existing allocationRow
	lookup := tx.NewRaw(`SELECT id, allocation_id, allocation_version, canonical_json, fingerprint FROM billing_allocations WHERE store_id = ? AND allocation_id = ? AND allocation_version = ? LIMIT 1`, s.storeID, canonical.ID, int64(canonical.Version)).Scan(ctx, &existing)
	if lookup == nil {
		if err := resolveAllocationReplay(existing, payload, fingerprint, canonical); err != nil {
			return err
		}
		if err := s.validateAllocationSupersessionHistory(ctx, tx, canonical); err != nil {
			return err
		}
		return nil
	}
	if !errors.Is(lookup, sql.ErrNoRows) {
		return fmt.Errorf("billingstore: allocation identity lookup: %w", lookup)
	}
	// Validate the complete immutable history before inserting a new row. This
	// resolves references supplied in any arrival order, rejects a conflicting
	// payload/scope/fork/cycle, and leaves an absent predecessor explicitly
	// pending for a later append.
	if err := s.validateAllocationSupersessionHistory(ctx, tx, canonical); err != nil {
		return err
	}
	sourceSubjectJSON, err := json.Marshal(canonical.SourceSubject)
	if err != nil {
		return fmt.Errorf("billingstore: allocation source subject JSON: %w", err)
	}
	sourceAmountCoefficient, sourceAmountScale, sourceAmountPresent := decimalProjection(canonical.SourceAmount)
	sourceQuantityCoefficient, sourceQuantityScale, sourceQuantityPresent := decimalProjection(canonical.SourceQuantity)
	roundedSourceNano, roundedSourceCurrency, roundedSourcePresent := allocationRoundedProjection(canonical.RoundedSourceAmount)
	sourceObservationRefs, err := jsonArray(canonical.SourceObservationRefs)
	if err != nil {
		return fmt.Errorf("billingstore: allocation source observation refs JSON: %w", err)
	}
	sourceValuationRefs, err := jsonArray(canonical.SourceValuationRefs)
	if err != nil {
		return fmt.Errorf("billingstore: allocation source valuation refs JSON: %w", err)
	}
	supersedes, err := jsonArray(canonical.Supersedes)
	if err != nil {
		return fmt.Errorf("billingstore: allocation supersedes JSON: %w", err)
	}
	_, err = tx.NewRaw(`INSERT INTO billing_allocations(
		store_id, allocation_id, allocation_version, revision, source_subject_kind, source_subject_id, source_subject_json, source_tenant_id, source_account_id, source_period_id, source_basis,
		source_amount_coefficient, source_amount_scale, source_amount_present, source_quantity_coefficient, source_quantity_scale, source_quantity_present, currency, unit,
		policy_method, policy_version, policy_hash, operation, rounding_scope, rounding_policy, rounding_residual_policy,
		rounded_source_nano, rounded_source_currency, rounded_source_present, rounding_residual_nano, source_observation_refs_json, source_valuation_refs_json, supersedes_json,
		canonical_json, fingerprint, projection_version, created_at_unix
	) VALUES (`+strings.TrimSuffix(strings.Repeat("?,", 37), ",")+`) ON CONFLICT DO NOTHING`,
		s.storeID, canonical.ID, int64(canonical.Version), int64(canonical.Revision), string(canonical.SourceSubject.Kind), subjectIDForEconomics(canonical.SourceSubject), string(sourceSubjectJSON), canonical.SourceSubject.TenantID, canonical.SourceSubject.AccountID, canonical.SourceSubject.PeriodID, string(canonical.SourceBasis),
		sourceAmountCoefficient, sourceAmountScale, boolProjection(tx, sourceAmountPresent), sourceQuantityCoefficient, sourceQuantityScale, boolProjection(tx, sourceQuantityPresent), canonical.Currency, canonical.Unit,
		canonical.Policy.Method, canonical.Policy.Version, canonical.Policy.Hash, string(canonical.Operation), string(canonical.RoundingScope), string(canonical.RoundingPolicy), string(canonical.RoundingResidualPolicy),
		roundedSourceNano, roundedSourceCurrency, boolProjection(tx, roundedSourcePresent), canonical.RoundingResidualNano, sourceObservationRefs, sourceValuationRefs, supersedes,
		string(payload), fingerprint, BillingEconomicsProjectionVersion, canonical.CreatedAt.UnixNano()).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: insert allocation: %w", err)
	}
	if err := tx.NewRaw(`SELECT id, allocation_id, allocation_version, canonical_json, fingerprint FROM billing_allocations WHERE store_id = ? AND allocation_id = ? AND allocation_version = ? LIMIT 1`, s.storeID, canonical.ID, int64(canonical.Version)).Scan(ctx, &existing); err != nil {
		return fmt.Errorf("billingstore: allocation row lookup after insert: %w", err)
	}
	if err := resolveAllocationReplay(existing, payload, fingerprint, canonical); err != nil {
		return err
	}
	for _, target := range canonical.Targets {
		if err := insertAllocationTarget(ctx, tx, s.storeID, canonical, target); err != nil {
			return err
		}
	}
	if err := s.economicFault("after_allocation"); err != nil {
		return err
	}
	return nil
}

// lockAllocationStore serializes supersession validation and insertion for a
// store. The canonical allocation rows remain immutable; this lock row only
// closes the validation gap between two writers that could otherwise both
// observe the same active head before either transaction commits.
func (s *DurableStore) lockAllocationStore(ctx context.Context, tx bun.Tx) error {
	if _, err := tx.NewRaw(`INSERT INTO billing_allocation_store_locks(store_id) VALUES (?) ON CONFLICT DO NOTHING`, s.storeID).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: allocation store lock insert: %w", err)
	}
	if tx.Dialect().Name() == dialect.PG {
		var storeID string
		if err := tx.NewRaw(`SELECT store_id FROM billing_allocation_store_locks WHERE store_id = ? FOR UPDATE`, s.storeID).Scan(ctx, &storeID); err != nil {
			return fmt.Errorf("billingstore: allocation store lock: %w", err)
		}
		return nil
	}
	if _, err := tx.NewRaw(`UPDATE billing_allocation_store_locks SET store_id = store_id WHERE store_id = ?`, s.storeID).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: allocation store lock: %w", err)
	}
	return nil
}

func isAllocationSQLiteContention(s *DurableStore, err error) bool {
	if s == nil || s.db == nil || s.db.Dialect().Name() != dialect.SQLite || err == nil {
		return false
	}
	return isSQLiteBusy(err) || strings.Contains(strings.ToLower(err.Error()), "deadlock")
}

func (s *DurableStore) validateAllocationSupersessionHistory(ctx context.Context, q bun.IDB, record economics.AllocationRecord) error {
	history, err := s.loadAllocationHistory(ctx, q)
	if err != nil {
		return err
	}
	history = append(history, record)
	if _, err := economics.ResolveAllocationSupersession(history); err != nil {
		return fmt.Errorf("billingstore: allocation supersession: %w", err)
	}
	return nil
}

// ResolveAllocationSupersession returns the typed fail-closed state for all
// durable allocation history. It reads canonical JSON only; projection rows
// cannot alter predecessor identity, source ownership, or pending status.
func (s *DurableStore) ResolveAllocationSupersession(ctx context.Context) (economics.AllocationSupersessionResult, error) {
	if err := s.validateContext(ctx); err != nil {
		return economics.AllocationSupersessionResult{}, err
	}
	history, err := s.loadAllocationHistory(ctx, s.db)
	if err != nil {
		return economics.AllocationSupersessionResult{}, err
	}
	return economics.ResolveAllocationSupersession(history)
}

func (s *DurableStore) loadAllocationHistory(ctx context.Context, q bun.IDB) ([]economics.AllocationRecord, error) {
	var rows []allocationRow
	if err := q.NewRaw(`SELECT id, allocation_id, allocation_version, canonical_json, fingerprint FROM billing_allocations WHERE store_id = ? ORDER BY id ASC`, s.storeID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("billingstore: load allocation history: %w", err)
	}
	history := make([]economics.AllocationRecord, 0, len(rows))
	for _, row := range rows {
		if row.AllocationVersion <= 0 {
			return nil, fmt.Errorf("%w: allocation row %d has invalid version", ErrIdentityConflict, row.ID)
		}
		var record economics.AllocationRecord
		if err := json.Unmarshal([]byte(row.CanonicalJSON), &record); err != nil {
			return nil, fmt.Errorf("billingstore: decode allocation history row %d: %w", row.ID, err)
		}
		canonical, err := record.Canonical()
		if err != nil {
			return nil, fmt.Errorf("billingstore: validate allocation history row %d: %w", row.ID, err)
		}
		if canonical.SourceSubject.StoreID != s.storeID {
			return nil, fmt.Errorf("%w: allocation row %d source subject store mismatch", ErrEconomicsOutOfScope, row.ID)
		}
		if canonical.ID != row.AllocationID || canonical.Version != uint64(row.AllocationVersion) {
			return nil, fmt.Errorf("%w: allocation row %d identity drift", ErrIdentityConflict, row.ID)
		}
		if row.Fingerprint != "" && row.Fingerprint != canonical.Fingerprint() {
			return nil, fmt.Errorf("%w: allocation row %d payload drift", ErrIdentityConflict, row.ID)
		}
		history = append(history, canonical)
	}
	return history, nil
}

func resolveAllocationReplay(existing allocationRow, payload []byte, fingerprint string, record economics.AllocationRecord) error {
	if existing.CanonicalJSON == string(payload) && (existing.Fingerprint == "" || existing.Fingerprint == fingerprint) {
		return nil
	}
	return fmt.Errorf("%w: allocation_id=%q version=%d", ErrIdentityConflict, record.ID, record.Version)
}

func allocationRoundedProjection(value *economics.AllocationRoundedAmount) (int64, string, int) {
	if value == nil {
		return 0, "", 0
	}
	return value.NanoUnits, value.Currency, boolInt(value.Present)
}

func insertAllocationTarget(ctx context.Context, q bun.IDB, storeID string, record economics.AllocationRecord, target economics.AllocationTarget) error {
	targetKind, targetSubjectID, targetJSON := "", "", "{}"
	targetTenantID, targetPoolID, targetWindowID := "", "", ""
	targetResetAt, targetStartAt, targetEndAt := int64(0), int64(0), int64(0)
	if !target.Unallocated {
		targetKind = string(target.Target.Kind)
		targetSubjectID = subjectIDForEconomics(target.Target)
		payload, err := json.Marshal(target.Target)
		if err != nil {
			return fmt.Errorf("billingstore: allocation target JSON: %w", err)
		}
		targetJSON = string(payload)
		targetTenantID = target.Target.TenantID
		targetPoolID = target.Target.PoolID
		targetWindowID = target.Target.WindowID
		targetResetAt = allocationTargetTimeProjection(target.Target.ResetAt)
		targetStartAt = allocationTargetTimeProjection(target.Target.StartAt)
		targetEndAt = allocationTargetTimeProjection(target.Target.EndAt)
	}
	roundedNano, roundedCurrency, roundedPresent := allocationRoundedProjection(target.RoundedAmount)
	_, err := q.NewRaw(`INSERT INTO billing_allocation_targets(
		store_id, allocation_id, allocation_version, target_id, target_kind, target_subject_id, target_json,
		target_tenant_id, target_pool_id, target_window_id, target_reset_at_unix, target_start_at_unix, target_end_at_unix,
		unallocated, informational,
		account_id, period_id, currency, unit, weight_numerator, weight_denominator, share_numerator, share_denominator,
		rounded_nano, rounded_currency, rounded_present, rounding_residual_nano, projection_version
	) VALUES (`+strings.TrimSuffix(strings.Repeat("?,", 28), ",")+`) ON CONFLICT DO NOTHING`,
		storeID, record.ID, int64(record.Version), target.TargetID, targetKind, targetSubjectID, targetJSON,
		targetTenantID, targetPoolID, targetWindowID, targetResetAt, targetStartAt, targetEndAt,
		boolProjection(q, boolInt(target.Unallocated)), boolProjection(q, boolInt(target.Informational)),
		target.AccountID, target.PeriodID, target.Currency, target.Unit, target.Weight.Numerator, target.Weight.Denominator, target.Share.Numerator, target.Share.Denominator,
		roundedNano, roundedCurrency, boolProjection(q, roundedPresent), target.RoundingResidualNano, BillingEconomicsProjectionVersion).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: insert allocation target: %w", err)
	}
	return nil
}

// GetAllocation loads the authoritative canonical envelope. Projection rows
// are intentionally not reassembled, so a stale projection cannot alter the
// source lineage or exact shares returned to callers.
func (s *DurableStore) GetAllocation(ctx context.Context, allocationID string, version uint64) (economics.AllocationRecord, error) {
	if err := s.validateContext(ctx); err != nil {
		return economics.AllocationRecord{}, err
	}
	if strings.TrimSpace(allocationID) == "" {
		return economics.AllocationRecord{}, fmt.Errorf("billingstore: allocation id is required")
	}
	if version == 0 || version > math.MaxInt64 {
		return economics.AllocationRecord{}, fmt.Errorf("billingstore: allocation version is invalid")
	}
	var payload string
	if err := s.db.NewRaw(`SELECT canonical_json FROM billing_allocations WHERE store_id = ? AND allocation_id = ? AND allocation_version = ? LIMIT 1`, s.storeID, allocationID, int64(version)).Scan(ctx, &payload); err != nil {
		return economics.AllocationRecord{}, fmt.Errorf("billingstore: get allocation: %w", err)
	}
	var record economics.AllocationRecord
	if err := json.Unmarshal([]byte(payload), &record); err != nil {
		return economics.AllocationRecord{}, fmt.Errorf("billingstore: decode allocation: %w", err)
	}
	canonical, err := record.Canonical()
	if err != nil {
		return economics.AllocationRecord{}, fmt.Errorf("billingstore: validate allocation: %w", err)
	}
	return canonical, nil
}

func (s *DurableStore) ListAllocations(ctx context.Context, query economics.AllocationQuery) (economics.AllocationPage, error) {
	if err := s.validateContext(ctx); err != nil {
		return economics.AllocationPage{}, err
	}
	storeID, err := economicsStoreID(query.StoreID, s.storeID)
	if err != nil {
		return economics.AllocationPage{}, err
	}
	if query.SourceSubject != nil {
		if err := query.SourceSubject.Validate(); err != nil {
			return economics.AllocationPage{}, fmt.Errorf("%w: source subject: %v", ErrEconomicsOutOfScope, err)
		}
		if query.SourceSubject.StoreID != storeID {
			return economics.AllocationPage{}, fmt.Errorf("%w: source subject store", ErrEconomicsOutOfScope)
		}
	}
	if query.TargetSubject != nil {
		if err := query.TargetSubject.Validate(); err != nil {
			return economics.AllocationPage{}, fmt.Errorf("%w: target subject: %v", ErrEconomicsOutOfScope, err)
		}
		if query.TargetSubject.StoreID != storeID {
			return economics.AllocationPage{}, fmt.Errorf("%w: target subject store", ErrEconomicsOutOfScope)
		}
	}
	if query.PolicyMethod != "" {
		if err := economics.ValidateSafeRef("allocation policy method", query.PolicyMethod); err != nil {
			return economics.AllocationPage{}, fmt.Errorf("%w: policy method", ErrEconomicsOutOfScope)
		}
	}
	if query.Operation != "" && !query.Operation.IsKnown() {
		return economics.AllocationPage{}, fmt.Errorf("%w: operation", ErrEconomicsOutOfScope)
	}
	if query.SourceSubject == nil && query.TargetSubject == nil && query.PolicyMethod == "" && query.Operation == "" {
		return economics.AllocationPage{}, ErrQueryTooBroad
	}
	limit, err := economicsLimit(query.Limit, 100)
	if err != nil {
		return economics.AllocationPage{}, err
	}
	sourceKind, sourceID := "", ""
	if query.SourceSubject != nil {
		sourceKind, sourceID = string(query.SourceSubject.Kind), subjectIDForEconomics(*query.SourceSubject)
	}
	targetKind, targetID := "", ""
	targetJSON := ""
	if query.TargetSubject != nil {
		canonicalTarget, payload, targetErr := allocationTargetSubjectForQuery(*query.TargetSubject)
		if targetErr != nil {
			return economics.AllocationPage{}, fmt.Errorf("%w: target subject: %v", ErrEconomicsOutOfScope, targetErr)
		}
		targetKind, targetID, targetJSON = string(canonicalTarget.Kind), subjectIDForEconomics(canonicalTarget), string(payload)
	}
	var hash string
	if query.TargetSubject != nil {
		// Keep the target binding as one canonical payload so every SubjectRef
		// dimension participates in cursor validation, including fields added to
		// the tagged union in the future only after canonical JSON changes.
		hash = economicsFilterHash(struct {
			StoreID, SourceKind, SourceID, TargetKind, TargetID, TargetJSON, PolicyMethod, Operation string
		}{storeID, sourceKind, sourceID, targetKind, targetID, targetJSON, query.PolicyMethod, string(query.Operation)})
	} else {
		// Source-only cursors retain the pre-target-scope hash and therefore keep
		// source query behavior compatible with existing callers.
		hash = economicsFilterHash(struct {
			StoreID, SourceKind, SourceID, TargetKind, TargetID, PolicyMethod, Operation string
		}{storeID, sourceKind, sourceID, targetKind, targetID, query.PolicyMethod, string(query.Operation)})
	}
	position, err := decodeEconomicsCursor(query.Cursor, "allocations", storeID, hash)
	if err != nil {
		return economics.AllocationPage{}, err
	}
	where := []string{"a.store_id = ?"}
	args := []any{storeID}
	if sourceKind != "" {
		where = append(where, "a.source_subject_kind = ?", "a.source_subject_id = ?", "a.source_tenant_id = ?", "a.source_account_id = ?", "a.source_period_id = ?")
		args = append(args, sourceKind, sourceID, query.SourceSubject.TenantID, query.SourceSubject.AccountID, query.SourceSubject.PeriodID)
	}
	if targetKind != "" {
		where = append(where, "EXISTS (SELECT 1 FROM billing_allocation_targets t WHERE t.store_id = a.store_id AND t.allocation_id = a.allocation_id AND t.allocation_version = a.allocation_version AND t.target_kind = ? AND t.target_subject_id = ? AND t.target_json = ?)")
		args = append(args, targetKind, targetID, targetJSON)
	}
	if query.PolicyMethod != "" {
		where = append(where, "a.policy_method = ?")
		args = append(args, query.PolicyMethod)
	}
	if query.Operation != "" {
		where = append(where, "a.operation = ?")
		args = append(args, string(query.Operation))
	}
	if query.Cursor != "" {
		where = append(where, `(a.created_at_unix > ? OR (a.created_at_unix = ? AND (a.allocation_id > ? OR (a.allocation_id = ? AND (a.allocation_version > ? OR (a.allocation_version = ? AND a.id > ?))))))`)
		args = append(args, position.CreatedAt, position.CreatedAt, position.RecordID, position.RecordID, position.RecordVersion, position.RecordVersion, position.RowID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.canonical_json, a.fingerprint, a.created_at_unix, a.allocation_id, a.allocation_version, a.id FROM billing_allocations a WHERE `+strings.Join(where, " AND ")+` ORDER BY a.created_at_unix ASC, a.allocation_id ASC, a.allocation_version ASC, a.id ASC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return economics.AllocationPage{}, fmt.Errorf("billingstore: list allocations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type listed struct {
		Payload, Fingerprint, AllocationID string
		CreatedAt, Version, RowID          int64
	}
	items := make([]listed, 0, limit+1)
	for rows.Next() {
		var item listed
		if err := rows.Scan(&item.Payload, &item.Fingerprint, &item.CreatedAt, &item.AllocationID, &item.Version, &item.RowID); err != nil {
			return economics.AllocationPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return economics.AllocationPage{}, err
	}
	page := economics.AllocationPage{Allocations: make([]economics.AllocationRecord, 0, minBillingInt(len(items), limit))}
	if len(items) > limit {
		last := items[limit-1]
		page.NextCursor = encodeEconomicsCursor(economicsCursor{Version: v2EconomicsCursorVersion, Kind: "allocations", StoreID: storeID, FilterHash: hash, CreatedAt: last.CreatedAt, RecordID: last.AllocationID, RecordVersion: last.Version, RowID: last.RowID})
		items = items[:limit]
	}
	for _, item := range items {
		var record economics.AllocationRecord
		if err := json.Unmarshal([]byte(item.Payload), &record); err != nil {
			return economics.AllocationPage{}, fmt.Errorf("billingstore: decode listed allocation: %w", err)
		}
		canonical, err := record.Canonical()
		if err != nil {
			return economics.AllocationPage{}, fmt.Errorf("billingstore: validate listed allocation: %w", err)
		}
		if item.Fingerprint != "" && item.Fingerprint != canonical.Fingerprint() {
			return economics.AllocationPage{}, fmt.Errorf("%w: allocation payload drift", ErrIdentityConflict)
		}
		if query.TargetSubject != nil {
			if err := s.validateAllocationTargetProjections(ctx, canonical); err != nil {
				return economics.AllocationPage{}, err
			}
		}
		page.Allocations = append(page.Allocations, canonical)
	}
	return page, nil
}

func allocationTargetSubjectForQuery(subject metering.SubjectRef) (metering.SubjectRef, []byte, error) {
	if err := subject.Validate(); err != nil {
		return metering.SubjectRef{}, nil, err
	}
	if !subject.ResetAt.IsZero() {
		subject.ResetAt = subject.ResetAt.UTC()
	}
	if !subject.StartAt.IsZero() {
		subject.StartAt = subject.StartAt.UTC()
	}
	if !subject.EndAt.IsZero() {
		subject.EndAt = subject.EndAt.UTC()
	}
	payload, err := json.Marshal(subject)
	if err != nil {
		return metering.SubjectRef{}, nil, fmt.Errorf("encode target subject: %w", err)
	}
	return subject, payload, nil
}

func (s *DurableStore) validateAllocationTargetProjections(ctx context.Context, record economics.AllocationRecord) error {
	var rows []allocationTargetProjection
	if err := s.db.NewRaw(`
		SELECT store_id, allocation_id, allocation_version, target_id, target_kind,
			target_subject_id, target_json, target_tenant_id, target_pool_id, target_window_id,
			target_reset_at_unix, target_start_at_unix, target_end_at_unix, account_id, period_id
		FROM billing_allocation_targets
		WHERE store_id = ? AND allocation_id = ? AND allocation_version = ?
		ORDER BY id ASC`, record.SourceSubject.StoreID, record.ID, int64(record.Version)).Scan(ctx, &rows); err != nil {
		return fmt.Errorf("billingstore: validate allocation target projections: %w", err)
	}
	if len(rows) != len(record.Targets) {
		return fmt.Errorf("%w: allocation target projection count drift", ErrIdentityConflict)
	}
	expected := make(map[string]economics.AllocationTarget, len(record.Targets))
	for _, target := range record.Targets {
		expected[target.TargetID] = target
	}
	for _, row := range rows {
		target, ok := expected[row.TargetID]
		if !ok {
			return fmt.Errorf("%w: allocation target projection id drift", ErrIdentityConflict)
		}
		kind, subjectID, targetJSON := "", "", "{}"
		tenantID, poolID, windowID := "", "", ""
		resetAt, startAt, endAt := int64(0), int64(0), int64(0)
		if !target.Unallocated {
			kind = string(target.Target.Kind)
			subjectID = subjectIDForEconomics(target.Target)
			payload, err := json.Marshal(target.Target)
			if err != nil {
				return fmt.Errorf("billingstore: encode allocation target projection: %w", err)
			}
			targetJSON = string(payload)
			tenantID, poolID, windowID = target.Target.TenantID, target.Target.PoolID, target.Target.WindowID
			resetAt = allocationTargetTimeProjection(target.Target.ResetAt)
			startAt = allocationTargetTimeProjection(target.Target.StartAt)
			endAt = allocationTargetTimeProjection(target.Target.EndAt)
		}
		if row.StoreID != record.SourceSubject.StoreID || row.AllocationID != record.ID || row.AllocationVersion != int64(record.Version) ||
			row.TargetKind != kind || row.TargetSubjectID != subjectID || row.TargetJSON != targetJSON ||
			row.TargetTenantID != tenantID || row.TargetPoolID != poolID || row.TargetWindowID != windowID ||
			row.TargetResetAt != resetAt || row.TargetStartAt != startAt || row.TargetEndAt != endAt ||
			row.AccountID != target.AccountID || row.PeriodID != target.PeriodID {
			return fmt.Errorf("%w: allocation target projection scope drift for %q", ErrIdentityConflict, row.TargetID)
		}
		delete(expected, row.TargetID)
	}
	if len(expected) != 0 {
		return fmt.Errorf("%w: allocation target projection missing target", ErrIdentityConflict)
	}
	return nil
}
