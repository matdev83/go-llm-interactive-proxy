package billingstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Historical V1 migration compatibility reader for Task 17.1 (Migration
// Strategy steps 1-3). This is a read-only compatibility preflight over the
// existing usage_call_records/usage_leg_records tables: it round-trips
// baseline V1 records with exact byte/hash/identity semantics, projects them
// as explicitly legacy/opaque views without inventing E/Q/P/S/R breakdown or
// local/provider source separation, and reports explicit V1 writer ownership
// for old in-flight calls. It performs no writes and never replays or mutates
// already-posted balance/journal effects. Malformed or ambiguous
// version/identity data fails closed. Durable cutover/posting enforcement
// belongs to Phase 17.3.

type historicalV1LegRow struct {
	Key         string `bun:"usage_leg_key"`
	Fingerprint string `bun:"fingerprint"`
	Payload     string `bun:"payload_json"`
}

type historicalV1CallRow struct {
	Key         string `bun:"usage_call_key"`
	Fingerprint string `bun:"fingerprint"`
	Payload     string `bun:"payload_json"`
}

func historicalV1PayloadSHA(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// ReadHistoricalV1Leg returns the legacy-opaque view of one baseline V1 leg
// through the new storage/query path. Stored key, fingerprint and payload
// bytes must match the sealed replay identity exactly; any drift, V2
// envelope, or ambiguous version fails closed.
func (s *DurableStore) ReadHistoricalV1Leg(ctx context.Context, callID billing.BillingCallID, bLegID string) (billing.HistoricalV1LegView, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.HistoricalV1LegView{}, err
	}
	if err := callID.Validate(); err != nil {
		return billing.HistoricalV1LegView{}, err
	}
	bLegID = strings.TrimSpace(bLegID)
	if bLegID == "" || strings.Contains(bLegID, ":") {
		return billing.HistoricalV1LegView{}, fmt.Errorf("%w: %w: historical V1 B-leg identity is invalid", billing.ErrHistoricalV1Ambiguous, billing.ErrInvalidRecord)
	}
	key, err := billing.CallLegUsageKey(callID, bLegID)
	if err != nil {
		return billing.HistoricalV1LegView{}, err
	}
	var row historicalV1LegRow
	if err := s.db.NewRaw(`SELECT usage_leg_key, fingerprint, payload_json FROM usage_leg_records WHERE usage_leg_key = ?`, key).Scan(ctx, &row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billing.HistoricalV1LegView{}, ErrUsageRecordNotFound
		}
		return billing.HistoricalV1LegView{}, fmt.Errorf("billingstore: read historical V1 leg: %w", err)
	}
	if strings.TrimSpace(row.Payload) == "" || strings.TrimSpace(row.Key) == "" || strings.TrimSpace(row.Fingerprint) == "" {
		return billing.HistoricalV1LegView{}, fmt.Errorf("%w: historical V1 leg row is incomplete", billing.ErrHistoricalV1Ambiguous)
	}
	if row.Key != key {
		return billing.HistoricalV1LegView{}, fmt.Errorf("%w: historical V1 leg key drift", billing.ErrHistoricalV1Ambiguous)
	}
	var record billing.CallLegUsageRecord
	if err := json.Unmarshal([]byte(row.Payload), &record); err != nil {
		return billing.HistoricalV1LegView{}, fmt.Errorf("%w: historical V1 leg decode: %v", billing.ErrHistoricalV1Ambiguous, err)
	}
	view, err := billing.ProjectHistoricalV1Leg(record)
	if err != nil {
		return billing.HistoricalV1LegView{}, err
	}
	if row.Fingerprint != view.Fingerprint || row.Key != view.Key {
		return billing.HistoricalV1LegView{}, fmt.Errorf("%w: historical V1 leg fingerprint drift", billing.ErrHistoricalV1Ambiguous)
	}
	if storedSHA := historicalV1PayloadSHA(row.Payload); storedSHA != view.PayloadSHA256 {
		return billing.HistoricalV1LegView{}, fmt.Errorf("%w: historical V1 leg payload drift", billing.ErrHistoricalV1Ambiguous)
	}
	return view, nil
}

// ReadHistoricalV1CallBundle returns one baseline V1 call with all of its V1
// legs as legacy-opaque views. The bundle preserves old replay identity and
// already-posted balance semantics by performing read-only SELECTs only; it
// never writes journals, balances, or posting fences.
func (s *DurableStore) ReadHistoricalV1CallBundle(ctx context.Context, callID billing.BillingCallID) (billing.HistoricalV1CallBundle, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.HistoricalV1CallBundle{}, err
	}
	if err := callID.Validate(); err != nil {
		return billing.HistoricalV1CallBundle{}, err
	}
	var callRow historicalV1CallRow
	if err := s.db.NewRaw(`SELECT usage_call_key, fingerprint, payload_json FROM usage_call_records WHERE call_id = ?`, callID.String()).Scan(ctx, &callRow); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billing.HistoricalV1CallBundle{}, ErrUsageRecordNotFound
		}
		return billing.HistoricalV1CallBundle{}, fmt.Errorf("billingstore: read historical V1 call: %w", err)
	}
	if strings.TrimSpace(callRow.Payload) == "" || strings.TrimSpace(callRow.Key) == "" || strings.TrimSpace(callRow.Fingerprint) == "" {
		return billing.HistoricalV1CallBundle{}, fmt.Errorf("%w: historical V1 call row is incomplete", billing.ErrHistoricalV1Ambiguous)
	}
	var call billing.CallUsageRecord
	if err := json.Unmarshal([]byte(callRow.Payload), &call); err != nil {
		return billing.HistoricalV1CallBundle{}, fmt.Errorf("%w: historical V1 call decode: %v", billing.ErrHistoricalV1Ambiguous, err)
	}
	callView, err := billing.ProjectHistoricalV1Call(call)
	if err != nil {
		return billing.HistoricalV1CallBundle{}, err
	}
	if callRow.Key != callView.Key || callRow.Fingerprint != callView.Fingerprint {
		return billing.HistoricalV1CallBundle{}, fmt.Errorf("%w: historical V1 call fingerprint drift", billing.ErrHistoricalV1Ambiguous)
	}
	if storedSHA := historicalV1PayloadSHA(callRow.Payload); storedSHA != callView.PayloadSHA256 {
		return billing.HistoricalV1CallBundle{}, fmt.Errorf("%w: historical V1 call payload drift", billing.ErrHistoricalV1Ambiguous)
	}

	var legRows []historicalV1LegRow
	if err := s.db.NewRaw(`SELECT usage_leg_key, fingerprint, payload_json FROM usage_leg_records WHERE call_id = ? ORDER BY b_leg_id ASC`, callID.String()).Scan(ctx, &legRows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return billing.HistoricalV1CallBundle{}, fmt.Errorf("billingstore: read historical V1 legs: %w", err)
	}
	if len(legRows) == 0 {
		return billing.HistoricalV1CallBundle{}, fmt.Errorf("%w: historical V1 call has no legs", billing.ErrHistoricalV1Ambiguous)
	}
	legs := make([]billing.HistoricalV1LegView, 0, len(legRows))
	records := make([]billing.CallLegUsageRecord, 0, len(legRows))
	for _, row := range legRows {
		if strings.TrimSpace(row.Payload) == "" || strings.TrimSpace(row.Key) == "" || strings.TrimSpace(row.Fingerprint) == "" {
			return billing.HistoricalV1CallBundle{}, fmt.Errorf("%w: historical V1 leg row is incomplete", billing.ErrHistoricalV1Ambiguous)
		}
		var record billing.CallLegUsageRecord
		if err := json.Unmarshal([]byte(row.Payload), &record); err != nil {
			return billing.HistoricalV1CallBundle{}, fmt.Errorf("%w: historical V1 leg decode: %v", billing.ErrHistoricalV1Ambiguous, err)
		}
		view, err := billing.ProjectHistoricalV1Leg(record)
		if err != nil {
			return billing.HistoricalV1CallBundle{}, err
		}
		if row.Key != view.Key || row.Fingerprint != view.Fingerprint {
			return billing.HistoricalV1CallBundle{}, fmt.Errorf("%w: historical V1 leg fingerprint drift", billing.ErrHistoricalV1Ambiguous)
		}
		if storedSHA := historicalV1PayloadSHA(row.Payload); storedSHA != view.PayloadSHA256 {
			return billing.HistoricalV1CallBundle{}, fmt.Errorf("%w: historical V1 leg payload drift", billing.ErrHistoricalV1Ambiguous)
		}
		legs = append(legs, view)
		records = append(records, record)
	}
	if err := billing.CheckHistoricalV1WriterClaim(records, billing.HistoricalV1WriterVersion); err != nil {
		return billing.HistoricalV1CallBundle{}, err
	}
	bundle := billing.HistoricalV1CallBundle{Call: callView, Legs: legs, WriterVersion: billing.HistoricalV1WriterVersion}
	if err := bundle.Validate(); err != nil {
		return billing.HistoricalV1CallBundle{}, err
	}
	return bundle, nil
}

// CheckHistoricalV1WriterClaim is a read-only ownership preflight for old
// in-flight V1 work at the storage seam. It validates the requested CallID
// against actual row key/fingerprint/payload identity: each row's key must
// match the requested call, its columns must match its sealed payload, and
// its payload CallID must match the request. Mixed-call, drifted, or
// malformed rows fail closed. A claimant other than the resolved owner fails
// with billing.ErrHistoricalV1WriterConflict. Durable epoch/posting
// enforcement belongs to Phase 17.3; this check performs no writes.
func (s *DurableStore) CheckHistoricalV1WriterClaim(ctx context.Context, callID billing.BillingCallID, claimant string) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	if err := callID.Validate(); err != nil {
		return err
	}
	if claimant != billing.HistoricalV1WriterVersion && claimant != billing.V2WriterVersion {
		return fmt.Errorf("%w: %w: unknown writer claimant %q", billing.ErrHistoricalV1Ambiguous, billing.ErrInvalidRecord, claimant)
	}
	var rows []historicalV1LegRow
	if err := s.db.NewRaw(`SELECT usage_leg_key, fingerprint, payload_json FROM usage_leg_records WHERE call_id = ? ORDER BY b_leg_id ASC`, callID.String()).Scan(ctx, &rows); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: historical writer claim has no legs", billing.ErrHistoricalV1Ambiguous)
		}
		return fmt.Errorf("billingstore: read historical writer claim legs: %w", err)
	}
	if len(rows) == 0 {
		return fmt.Errorf("%w: historical writer claim has no legs", billing.ErrHistoricalV1Ambiguous)
	}
	legs := make([]billing.CallLegUsageRecord, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Payload) == "" || strings.TrimSpace(row.Key) == "" || strings.TrimSpace(row.Fingerprint) == "" {
			return fmt.Errorf("%w: historical writer claim row is incomplete", billing.ErrHistoricalV1Ambiguous)
		}
		var record billing.CallLegUsageRecord
		if err := json.Unmarshal([]byte(row.Payload), &record); err != nil {
			return fmt.Errorf("%w: historical writer claim decode: %v", billing.ErrHistoricalV1Ambiguous, err)
		}
		if record.CallID != callID {
			return fmt.Errorf("%w: %w: historical writer claim payload call mismatch", billing.ErrHistoricalV1Ambiguous, billing.ErrInvalidRecord)
		}
		wantKey, err := billing.CallLegUsageKey(callID, record.BLegID)
		if err != nil {
			return err
		}
		if row.Key != wantKey || row.Key != record.Key {
			return fmt.Errorf("%w: %w: historical writer claim key mismatch", billing.ErrHistoricalV1Ambiguous, billing.ErrInvalidRecord)
		}
		if row.Fingerprint != record.Fingerprint {
			return fmt.Errorf("%w: %w: historical writer claim fingerprint mismatch", billing.ErrHistoricalV1Ambiguous, billing.ErrInvalidRecord)
		}
		// Validate the original stored envelope before Seal normalization,
		// which would restore a missing V2 projection label.
		if _, err := billing.WriterVersionForLeg(record); err != nil {
			return err
		}
		sealed, err := record.Seal()
		if err != nil {
			return err
		}
		if sealed.Key != row.Key || sealed.Fingerprint != row.Fingerprint {
			return fmt.Errorf("%w: %w: historical writer claim replay mismatch", billing.ErrHistoricalV1Ambiguous, billing.ErrInvalidRecord)
		}
		legs = append(legs, record)
	}
	return billing.CheckHistoricalV1WriterClaim(legs, claimant)
}
