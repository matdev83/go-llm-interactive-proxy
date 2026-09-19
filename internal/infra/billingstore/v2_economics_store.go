package billingstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const (
	v2EconomicsCursorPrefix    = "v2."
	v2EconomicsCursorVersion   = 1
	v2EconomicsHardPageMaximum = 500

	// ReconciliationRecordSchemaLegacy is the original single-basis
	// reconciliation envelope.
	ReconciliationRecordSchemaLegacy uint32 = 1
	// ReconciliationRecordSchemaRetention is the full-result retention
	// envelope; its result payload is billing.ReconciliationRetentionResult.
	ReconciliationRecordSchemaRetention uint32 = 2
)

var (
	// ErrInvalidEconomicsCursor identifies a malformed or filter-bound cursor.
	ErrInvalidEconomicsCursor = errors.New("billingstore: invalid economics cursor")
	// ErrEconomicsPageSizeExceeded identifies a request above the hard bounded
	// pagination limit.
	ErrEconomicsPageSizeExceeded = errors.New("billingstore: economics page size exceeds hard maximum")
	// ErrEconomicsOutOfScope identifies a query or record from another store.
	ErrEconomicsOutOfScope = errors.New("billingstore: economics record out of scope")
	// ErrCrossDatabaseTransaction prevents composition from pretending two Bun
	// handles are one atomic local transaction.
	ErrCrossDatabaseTransaction = errors.New("billingstore: composition requires one local database handle")
	ErrInvalidReconciliation    = errors.New("billingstore: invalid reconciliation")
	ErrInvalidEconomicWork      = errors.New("billingstore: invalid economic work marker")
	ErrQueryTooBroad            = metering.ErrQueryTooBroad
)

// ReconciliationRecord is the minimal immutable V2 reconciliation envelope.
// ResultJSON is canonical JSON and can contain a domain-specific result shape;
// the storage layer does not infer provider or customer semantics from it.
type ReconciliationRecord struct {
	ID      string
	Version uint64
	Subject metering.SubjectRef
	Scope   string
	Basis   economics.ValuationBasis
	// ResultSchemaVersion distinguishes the original single-basis envelope
	// from the full-result retention envelope. Zero is normalized to the
	// legacy envelope version.
	ResultSchemaVersion uint32
	InputSetHash        string
	InputHash           string // compatibility alias for InputSetHash
	LocalInputHash      string
	ProviderInputHash   string
	PolicyID            string
	PolicyVersion       string
	ResultJSON          json.RawMessage
	Result              json.RawMessage // compatibility alias for ResultJSON
	CreatedAt           time.Time
}

// Reconciliation is a short alias for ReconciliationRecord.
type Reconciliation = ReconciliationRecord

type reconciliationWire struct {
	ID                  string                   `json:"id"`
	Version             uint64                   `json:"version"`
	Subject             metering.SubjectRef      `json:"subject"`
	Scope               string                   `json:"scope,omitempty"`
	Basis               economics.ValuationBasis `json:"basis"`
	ResultSchemaVersion uint32                   `json:"result_schema_version,omitempty"`
	InputSetHash        string                   `json:"input_set_hash,omitempty"`
	LocalInputHash      string                   `json:"local_input_hash,omitempty"`
	ProviderInputHash   string                   `json:"provider_input_hash,omitempty"`
	PolicyID            string                   `json:"policy_id,omitempty"`
	PolicyVersion       string                   `json:"policy_version,omitempty"`
	ResultJSON          json.RawMessage          `json:"result"`
	CreatedAt           time.Time                `json:"created_at"`
}

func (r ReconciliationRecord) normalized() (ReconciliationRecord, []byte, error) {
	out := r
	if out.ID == "" || strings.TrimSpace(out.ID) != out.ID {
		return ReconciliationRecord{}, nil, fmt.Errorf("%w: id is required", ErrInvalidReconciliation)
	}
	if out.Version == 0 {
		return ReconciliationRecord{}, nil, fmt.Errorf("%w: version is required", ErrInvalidReconciliation)
	}
	if err := out.Subject.Validate(); err != nil {
		return ReconciliationRecord{}, nil, fmt.Errorf("%w: subject: %v", ErrInvalidReconciliation, err)
	}
	if out.ResultSchemaVersion == 0 {
		out.ResultSchemaVersion = ReconciliationRecordSchemaLegacy
	}
	switch out.ResultSchemaVersion {
	case ReconciliationRecordSchemaLegacy:
		if err := out.Basis.Validate(); err != nil {
			return ReconciliationRecord{}, nil, fmt.Errorf("%w: basis: %v", ErrInvalidReconciliation, err)
		}
	case ReconciliationRecordSchemaRetention:
		if out.Basis != "" {
			return ReconciliationRecord{}, nil, fmt.Errorf("%w: retention record cannot declare a single basis", ErrInvalidReconciliation)
		}
	default:
		return ReconciliationRecord{}, nil, fmt.Errorf("%w: unknown result schema version %d", ErrInvalidReconciliation, out.ResultSchemaVersion)
	}
	if out.InputSetHash == "" {
		out.InputSetHash = out.InputHash
	} else if out.InputHash != "" && out.InputHash != out.InputSetHash {
		return ReconciliationRecord{}, nil, fmt.Errorf("%w: input hash aliases disagree", ErrInvalidReconciliation)
	}
	out.InputHash = out.InputSetHash
	for name, value := range map[string]string{
		"input_set_hash": out.InputSetHash, "local_input_hash": out.LocalInputHash,
		"provider_input_hash": out.ProviderInputHash,
	} {
		if value != "" {
			decoded, err := hex.DecodeString(value)
			if err != nil || len(decoded) != sha256.Size {
				return ReconciliationRecord{}, nil, fmt.Errorf("%w: %s must be SHA-256 hex", ErrInvalidReconciliation, name)
			}
		}
	}
	result := out.ResultJSON
	if len(result) == 0 {
		result = out.Result
	}
	if len(result) == 0 {
		return ReconciliationRecord{}, nil, fmt.Errorf("%w: result JSON is required", ErrInvalidReconciliation)
	}
	canonicalResult, err := canonicalJSON(result)
	if err != nil {
		return ReconciliationRecord{}, nil, fmt.Errorf("%w: result JSON: %v", ErrInvalidReconciliation, err)
	}
	out.ResultJSON = canonicalResult
	out.Result = append(json.RawMessage(nil), canonicalResult...)
	if out.CreatedAt.IsZero() {
		out.CreatedAt = time.Now().UTC()
	}
	wire := reconciliationWire{ID: out.ID, Version: out.Version, Subject: out.Subject, Scope: out.Scope, Basis: out.Basis, ResultSchemaVersion: reconciliationWireSchemaVersion(out.ResultSchemaVersion), InputSetHash: out.InputSetHash, LocalInputHash: out.LocalInputHash, ProviderInputHash: out.ProviderInputHash, PolicyID: out.PolicyID, PolicyVersion: out.PolicyVersion, ResultJSON: canonicalResult, CreatedAt: out.CreatedAt}
	payload, err := json.Marshal(wire)
	if err != nil {
		return ReconciliationRecord{}, nil, fmt.Errorf("%w: canonical JSON: %v", ErrInvalidReconciliation, err)
	}
	return out, payload, nil
}

// reconciliationWireSchemaVersion keeps the durable wire backward compatible.
// Schema-1 records were serialized before the field existed, so they must keep
// omitting it; only the full-result retention generation is emitted.
func reconciliationWireSchemaVersion(version uint32) uint32 {
	if version >= ReconciliationRecordSchemaRetention {
		return version
	}
	return 0
}

// durableReconciliationGeneration normalizes the stored
// result_schema_version discriminator the same way the canonical wire
// normalizes a field-less generation: an explicit zero is the legacy
// generation. Any other value outside the two known generations fails closed.
func durableReconciliationGeneration(column int64) (uint32, error) {
	if column < 0 || column > int64(math.MaxUint32) {
		return 0, fmt.Errorf("%w: unknown result schema version %d", ErrInvalidReconciliation, column)
	}
	generation := uint32(column)
	if generation == 0 {
		return ReconciliationRecordSchemaLegacy, nil
	}
	switch generation {
	case ReconciliationRecordSchemaLegacy, ReconciliationRecordSchemaRetention:
		return generation, nil
	default:
		return 0, fmt.Errorf("%w: unknown result schema version %d", ErrInvalidReconciliation, column)
	}
}

// bindReconciliationReadGeneration is the single generation gate for a decoded
// canonical wire: the decoded wire generation must equal the durable
// discriminator before the caller may assign the stored value or bind a
// projection, so a cross-generation row can never be returned.
func bindReconciliationReadGeneration(wireGeneration uint32, column int64) (uint32, error) {
	durable, err := durableReconciliationGeneration(column)
	if err != nil {
		return 0, err
	}
	if wireGeneration != durable {
		return 0, fmt.Errorf("%w: canonical wire generation %d does not match durable generation %d", ErrReconciliationRetentionMismatch, wireGeneration, durable)
	}
	return durable, nil
}

func (r ReconciliationRecord) CanonicalJSON() ([]byte, error) {
	_, payload, err := r.normalized()
	return payload, err
}

func decodeCanonicalReconciliation(payload []byte) (ReconciliationRecord, []byte, error) {
	var wire reconciliationWire
	if err := json.Unmarshal(payload, &wire); err != nil {
		return ReconciliationRecord{}, nil, err
	}
	record := ReconciliationRecord{
		ID:                  wire.ID,
		Version:             wire.Version,
		Subject:             wire.Subject,
		Scope:               wire.Scope,
		Basis:               wire.Basis,
		ResultSchemaVersion: wire.ResultSchemaVersion,
		InputSetHash:        wire.InputSetHash,
		LocalInputHash:      wire.LocalInputHash,
		ProviderInputHash:   wire.ProviderInputHash,
		PolicyID:            wire.PolicyID,
		PolicyVersion:       wire.PolicyVersion,
		ResultJSON:          append(json.RawMessage(nil), wire.ResultJSON...),
		CreatedAt:           wire.CreatedAt,
	}
	normalized, canonicalJSON, err := record.normalized()
	if err != nil {
		return ReconciliationRecord{}, nil, err
	}
	if normalized.ResultSchemaVersion == ReconciliationRecordSchemaRetention {
		// A schema-2 canonical envelope must be the authoritative mapping of
		// its inner full result; a mismatched envelope fails closed on read
		// instead of being repaired or trusted.
		result, err := billing.ParseReconciliationRetentionResult(normalized.ResultJSON)
		if err != nil {
			return ReconciliationRecord{}, nil, fmt.Errorf("%w: retention result payload: %w", ErrInvalidReconciliation, err)
		}
		if err := bindReconciliationRetentionEnvelope(normalized, result); err != nil {
			return ReconciliationRecord{}, nil, err
		}
	}
	return normalized, canonicalJSON, nil
}

func (r ReconciliationRecord) Fingerprint() string {
	payload, err := r.CanonicalJSON()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// EconomicWorkMarker is a durable, immutable worker marker. It is not a
// financial journal and has no balance or money mutation fields.
type EconomicWorkMarker struct {
	ID           string
	Version      uint64
	Kind         string
	SubjectKind  metering.SubjectKind
	SubjectID    string
	InputSetHash string
	PayloadJSON  json.RawMessage
	Status       string
	CreatedAt    time.Time
}

func (w EconomicWorkMarker) normalized() (EconomicWorkMarker, []byte, error) {
	if strings.TrimSpace(w.ID) == "" || strings.TrimSpace(w.ID) != w.ID {
		return EconomicWorkMarker{}, nil, fmt.Errorf("%w: id is required", ErrInvalidEconomicWork)
	}
	if w.Version == 0 || strings.TrimSpace(w.Kind) == "" {
		return EconomicWorkMarker{}, nil, fmt.Errorf("%w: version and kind are required", ErrInvalidEconomicWork)
	}
	if w.SubjectKind != "" && !w.SubjectKind.IsKnown() {
		return EconomicWorkMarker{}, nil, fmt.Errorf("%w: unknown subject kind", ErrInvalidEconomicWork)
	}
	if w.InputSetHash != "" {
		decoded, err := hex.DecodeString(w.InputSetHash)
		if err != nil || len(decoded) != sha256.Size {
			return EconomicWorkMarker{}, nil, fmt.Errorf("%w: input set hash must be SHA-256 hex", ErrInvalidEconomicWork)
		}
	}
	payload := w.PayloadJSON
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	canonicalPayload, err := canonicalJSON(payload)
	if err != nil {
		return EconomicWorkMarker{}, nil, fmt.Errorf("%w: payload JSON: %v", ErrInvalidEconomicWork, err)
	}
	out := w
	out.PayloadJSON = canonicalPayload
	out.Status = strings.TrimSpace(out.Status)
	if out.Status == "" {
		out.Status = "pending"
	}
	if out.CreatedAt.IsZero() {
		out.CreatedAt = time.Now().UTC()
	}
	return out, canonicalPayload, nil
}

// ValuationQuery scopes a bounded valuation listing.
type ValuationQuery struct {
	StoreID      string
	SubjectKind  metering.SubjectKind
	SubjectID    string
	Subject      *metering.SubjectRef
	TenantID     string
	Scope        string
	Basis        economics.ValuationBasis
	InputSetHash string
	Limit        int
	Cursor       string
}

type ValuationPage struct {
	Valuations []economics.Valuation
	NextCursor string
}

type ReconciliationQuery struct {
	StoreID      string
	SubjectKind  metering.SubjectKind
	SubjectID    string
	Subject      *metering.SubjectRef
	TenantID     string
	Scope        string
	Basis        economics.ValuationBasis
	InputSetHash string
	Limit        int
	Cursor       string
}

type ReconciliationPage struct {
	Reconciliations []ReconciliationRecord
	NextCursor      string
}

type economicsCursor struct {
	Version       int    `json:"version"`
	Kind          string `json:"kind"`
	StoreID       string `json:"store_id"`
	FilterHash    string `json:"filter_hash"`
	CreatedAt     int64  `json:"created_at"`
	RecordID      string `json:"record_id"`
	RecordVersion int64  `json:"record_version"`
	RowID         int64  `json:"row_id"`
}

func canonicalJSON(raw []byte) ([]byte, error) {
	if !json.Valid(raw) {
		return nil, errors.New("invalid JSON")
	}
	// Decode with UseNumber so an opaque result/payload containing a large
	// integer is not silently rounded through float64. Canonical economics
	// JSON must preserve the exact JSON number lexeme while still sorting object
	// keys through encoding/json's deterministic map encoder.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return json.Marshal(value)
}

func economicsStoreID(storeID, opened string) (string, error) {
	storeID = strings.TrimSpace(storeID)
	if storeID == "" {
		storeID = opened
	}
	if storeID != opened {
		return "", fmt.Errorf("%w: store_id=%q", ErrEconomicsOutOfScope, storeID)
	}
	return storeID, nil
}

func economicsSubjectValues(kind metering.SubjectKind, id string, subject *metering.SubjectRef) (metering.SubjectKind, string, error) {
	if subject != nil {
		if err := subject.Validate(); err != nil {
			return "", "", fmt.Errorf("%w: subject: %v", ErrEconomicsOutOfScope, err)
		}
		if kind != "" && kind != subject.Kind {
			return "", "", fmt.Errorf("%w: subject kind mismatch", ErrEconomicsOutOfScope)
		}
		if id != "" && id != subjectIDForEconomics(*subject) {
			return "", "", fmt.Errorf("%w: subject id mismatch", ErrEconomicsOutOfScope)
		}
		kind, id = subject.Kind, subjectIDForEconomics(*subject)
	}
	if kind != "" && !kind.IsKnown() {
		return "", "", fmt.Errorf("%w: unknown subject kind", ErrEconomicsOutOfScope)
	}
	if (kind == "") != (id == "") {
		return "", "", fmt.Errorf("%w: subject kind and id must be supplied together", ErrEconomicsOutOfScope)
	}
	return kind, id, nil
}

func (s *DurableStore) validateContext(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	if ctx == nil {
		return fmt.Errorf("billingstore: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func subjectIDForEconomics(subject metering.SubjectRef) string {
	switch subject.Kind {
	case metering.SubjectALeg:
		return subject.ALegID
	case metering.SubjectRequest:
		return subject.RequestID
	case metering.SubjectBillingCall:
		if subject.BillingCallID != "" {
			return subject.BillingCallID
		}
		return subject.CallID
	case metering.SubjectBLeg:
		return subject.BLegID
	case metering.SubjectSubmission:
		return subject.SubmissionID
	case metering.SubjectProviderCharge:
		return subject.ProviderChargeID
	case metering.SubjectProviderDebit:
		return subject.BLegID
	case metering.SubjectResource:
		return subject.ResourceID
	case metering.SubjectAccountWindow:
		return subject.WindowID
	case metering.SubjectStatementLine:
		return subject.StatementLineID
	default:
		return ""
	}
}

func economicsLimit(limit, defaultLimit int) (int, error) {
	if limit < 0 || limit > v2EconomicsHardPageMaximum {
		return 0, ErrEconomicsPageSizeExceeded
	}
	if limit == 0 {
		limit = defaultLimit
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > v2EconomicsHardPageMaximum {
		limit = v2EconomicsHardPageMaximum
	}
	return limit, nil
}

func economicsFilterHash(value any) string {
	b, _ := json.Marshal(value)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func encodeEconomicsCursor(cursor economicsCursor) string {
	b, _ := json.Marshal(cursor)
	return v2EconomicsCursorPrefix + base64.RawURLEncoding.EncodeToString(b)
}

func decodeEconomicsCursor(raw, kind, storeID, hash string) (economicsCursor, error) {
	if raw == "" {
		return economicsCursor{}, nil
	}
	if !strings.HasPrefix(raw, v2EconomicsCursorPrefix) {
		return economicsCursor{}, ErrInvalidEconomicsCursor
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, v2EconomicsCursorPrefix))
	if err != nil {
		return economicsCursor{}, fmt.Errorf("%w: base64", ErrInvalidEconomicsCursor)
	}
	var cursor economicsCursor
	if err := json.Unmarshal(b, &cursor); err != nil {
		return economicsCursor{}, fmt.Errorf("%w: JSON", ErrInvalidEconomicsCursor)
	}
	if cursor.Version != v2EconomicsCursorVersion || cursor.Kind != kind || cursor.StoreID != storeID || cursor.FilterHash != hash || cursor.RowID <= 0 || cursor.RecordID == "" || cursor.RecordVersion <= 0 {
		return economicsCursor{}, ErrInvalidEconomicsCursor
	}
	return cursor, nil
}

type valuationRow struct {
	ID               int64  `bun:"id"`
	StoreID          string `bun:"store_id"`
	ValuationID      string `bun:"valuation_id"`
	ValuationVersion int64  `bun:"valuation_version"`
	InputSetHash     string `bun:"input_set_hash"`
	CanonicalJSON    string `bun:"canonical_json"`
	Fingerprint      string `bun:"fingerprint"`
}

// AppendValuation stores the canonical immutable V2 valuation and its derived
// line projections atomically.
func (s *DurableStore) AppendValuation(ctx context.Context, valuation economics.Valuation) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: valuation begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.AppendValuationInTx(ctx, tx, valuation); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: valuation commit: %w", err)
	}
	return nil
}

func canonicalValuationForStore(storeID string, valuation economics.Valuation) (economics.Valuation, []byte, error) {
	if valuation.Subject.StoreID != storeID {
		return economics.Valuation{}, nil, fmt.Errorf("%w: valuation subject store", ErrEconomicsOutOfScope)
	}
	if valuation.Version != economics.ValuationVersionV2 {
		return economics.Valuation{}, nil, fmt.Errorf("%w: valuation version %d want %d", economics.ErrInvalidValuation, valuation.Version, economics.ValuationVersionV2)
	}
	// Verify and fill the input identity before public valuation validation so
	// a trusted durable append can accept the same empty-hash compatibility
	// state as a direct post-usage rater. A non-empty caller hash is rejected
	// before it can affect canonical JSON, lookup or uniqueness.
	verifiedHash, err := economics.CanonicalInputSetHash(valuation.Basis, valuation.InputObservations)
	if err != nil {
		return economics.Valuation{}, nil, err
	}
	if valuation.InputSetHash != "" && valuation.InputSetHash != verifiedHash {
		return economics.Valuation{}, nil, fmt.Errorf("%w: valuation basis=%q supplied=%q expected=%q", economics.ErrInputSetHashMismatch, valuation.Basis, valuation.InputSetHash, verifiedHash)
	}
	valuation.InputSetHash = verifiedHash
	canonical, err := valuation.Canonical()
	if err != nil {
		return economics.Valuation{}, nil, err
	}
	// Durable lookup and uniqueness must use the same canonical observation
	// preimage trusted by direct raters. New/legacy-compatible empty hashes are
	// filled from retained refs; a supplied non-empty mismatch is rejected
	// before it can influence either the identity query or persisted payload.
	verifiedHash, err = economics.CanonicalInputSetHash(canonical.Basis, canonical.InputObservations)
	if err != nil {
		return economics.Valuation{}, nil, err
	}
	if canonical.InputSetHash != "" && canonical.InputSetHash != verifiedHash {
		return economics.Valuation{}, nil, fmt.Errorf("%w: valuation basis=%q supplied=%q expected=%q", economics.ErrInputSetHashMismatch, canonical.Basis, canonical.InputSetHash, verifiedHash)
	}
	canonical.InputSetHash = verifiedHash
	payload, err := canonical.CanonicalJSON()
	if err != nil {
		return economics.Valuation{}, nil, err
	}
	return canonical, payload, nil
}

// canonicalLegacyValuationForStore preserves the canonical payload shape of a
// pre-input-hash V2 valuation. Empty InputSetHash is an explicit historical
// compatibility state: it is valid only when replaying an existing durable
// row that also has an empty stored hash. New rows still go through
// canonicalValuationForStore, which verifies and fills the hash.
func canonicalLegacyValuationForStore(storeID string, valuation economics.Valuation) (economics.Valuation, []byte, error) {
	if valuation.Subject.StoreID != storeID {
		return economics.Valuation{}, nil, fmt.Errorf("%w: valuation subject store", ErrEconomicsOutOfScope)
	}
	if valuation.Version != economics.ValuationVersionV2 {
		return economics.Valuation{}, nil, fmt.Errorf("%w: valuation version %d want %d", economics.ErrInvalidValuation, valuation.Version, economics.ValuationVersionV2)
	}
	canonical, err := valuation.Canonical()
	if err != nil {
		return economics.Valuation{}, nil, err
	}
	payload, err := canonical.CanonicalJSON()
	if err != nil {
		return economics.Valuation{}, nil, err
	}
	return canonical, payload, nil
}

func valuationContextHash(valuation economics.Valuation) string {
	return valuation.ContextHash()
}

func (s *DurableStore) AppendValuationInTx(ctx context.Context, tx bun.Tx, valuation economics.Valuation) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	var existing valuationRow
	existingErr := sql.ErrNoRows
	// Look up the immutable identity before strict hash verification so an
	// empty-hash caller can replay a valid historical row byte-for-byte. This
	// probe is deliberately limited to a structurally addressable identity;
	// normal validation remains owned by canonicalValuationForStore below.
	if valuation.ID != "" && valuation.Version != 0 {
		existingErr = tx.NewRaw(`SELECT id, store_id, valuation_id, valuation_version, input_set_hash, canonical_json, fingerprint FROM billing_valuations WHERE store_id = ? AND valuation_id = ? AND valuation_version = ? LIMIT 1`, s.storeID, valuation.ID, int64(valuation.Version)).Scan(ctx, &existing)
		if existingErr != nil && !errors.Is(existingErr, sql.ErrNoRows) {
			return fmt.Errorf("billingstore: valuation identity lookup: %w", existingErr)
		}
		if existingErr == nil && valuation.InputSetHash == "" && existing.InputSetHash == "" {
			legacy, payload, err := canonicalLegacyValuationForStore(s.storeID, valuation)
			if err != nil {
				return err
			}
			return resolveValuationReplay(existing, payload, legacy)
		}
	}
	canonical, payload, err := canonicalValuationForStore(s.storeID, valuation)
	if err != nil {
		return err
	}
	fingerprint := canonical.Fingerprint()
	contextHash := valuationContextHash(canonical)
	if existingErr == nil {
		return resolveValuationReplay(existing, payload, canonical)
	}
	if canonical.InputSetHash != "" {
		if err := tx.NewRaw(`SELECT id, store_id, valuation_id, valuation_version, input_set_hash, canonical_json, fingerprint FROM billing_valuations WHERE store_id = ? AND input_set_hash = ? AND valuation_context_hash = ? AND rater_id = ? AND rater_version = ? AND policy_id = ? AND policy_version = ? AND tariff_id = ? AND tariff_version = ? AND basis = ? LIMIT 1`, s.storeID, canonical.InputSetHash, contextHash, canonical.Rater.RaterID, canonical.Rater.Version, canonical.Policy.PolicyID, canonical.Policy.Version, canonical.Tariff.ID, canonical.Tariff.Version, string(canonical.Basis)).Scan(ctx, &existing); err == nil {
			if existing.CanonicalJSON == string(payload) {
				return fmt.Errorf("%w: valuation identity is already used by %q", ErrIdentityConflict, existing.ValuationID)
			}
			return fmt.Errorf("%w: valuation input set identity", ErrIdentityConflict)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("billingstore: valuation input identity lookup: %w", err)
		}
	}
	if _, err := tx.NewRaw(`
INSERT INTO billing_valuations(
	store_id, valuation_id, valuation_version, perspective, basis, subject_kind, subject_id, tenant_id, scope, input_set_hash,
	rater_id, rater_version, tariff_id, tariff_version, policy_id, policy_version, qualifier_snapshot, canonical_json, fingerprint, projection_version, created_at_unix, valuation_context_hash
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING
	`, s.storeID, canonical.ID, int64(canonical.Version), string(canonical.Perspective), string(canonical.Basis), string(canonical.Subject.Kind), subjectIDForEconomics(canonical.Subject), canonical.Subject.TenantID, canonical.Scope, canonical.InputSetHash,
		canonical.Rater.RaterID, canonical.Rater.Version, canonical.Tariff.ID, canonical.Tariff.Version, canonical.Policy.PolicyID, canonical.Policy.Version, canonical.QualifierSnapshot, string(payload), fingerprint, BillingEconomicsProjectionVersion, canonical.CreatedAt.UnixNano(), contextHash).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: insert valuation: %w", err)
	}
	if err := tx.NewRaw(`SELECT id, store_id, valuation_id, valuation_version, input_set_hash, canonical_json, fingerprint FROM billing_valuations WHERE store_id = ? AND valuation_id = ? AND valuation_version = ? LIMIT 1`, s.storeID, canonical.ID, int64(canonical.Version)).Scan(ctx, &existing); err != nil {
		if errors.Is(err, sql.ErrNoRows) && canonical.InputSetHash != "" {
			if inputErr := tx.NewRaw(`SELECT id, store_id, valuation_id, valuation_version, input_set_hash, canonical_json, fingerprint FROM billing_valuations WHERE store_id = ? AND input_set_hash = ? AND valuation_context_hash = ? AND rater_id = ? AND rater_version = ? AND policy_id = ? AND policy_version = ? AND tariff_id = ? AND tariff_version = ? AND basis = ? LIMIT 1`, s.storeID, canonical.InputSetHash, contextHash, canonical.Rater.RaterID, canonical.Rater.Version, canonical.Policy.PolicyID, canonical.Policy.Version, canonical.Tariff.ID, canonical.Tariff.Version, string(canonical.Basis)).Scan(ctx, &existing); inputErr == nil {
				return fmt.Errorf("%w: valuation input set identity is already used by %q", ErrIdentityConflict, existing.ValuationID)
			} else if !errors.Is(inputErr, sql.ErrNoRows) {
				return fmt.Errorf("billingstore: valuation input identity lookup after insert: %w", inputErr)
			}
		}
		return fmt.Errorf("billingstore: valuation row lookup after insert: %w", err)
	}
	if err := resolveValuationReplay(existing, payload, canonical); err != nil {
		return err
	}
	if err := insertValuationLines(ctx, tx, s.storeID, canonical); err != nil {
		return err
	}
	if err := s.economicFault("after_valuation"); err != nil {
		return err
	}
	return nil
}

func resolveValuationReplay(existing valuationRow, payload []byte, valuation economics.Valuation) error {
	if existing.CanonicalJSON == string(payload) && (existing.Fingerprint == "" || existing.Fingerprint == valuation.Fingerprint()) {
		return nil
	}
	return fmt.Errorf("%w: valuation_id=%q version=%d", ErrIdentityConflict, valuation.ID, valuation.Version)
}

func insertValuationLines(ctx context.Context, q bun.IDB, storeID string, valuation economics.Valuation) error {
	for _, line := range valuation.Lines {
		if err := insertValuationLine(ctx, q, storeID, valuation.ID, valuation.Version, line); err != nil {
			return err
		}
	}
	return nil
}

func decimalProjection(value *metering.Decimal) (string, int, int) {
	if value == nil {
		return "", 0, 0
	}
	normalized, err := value.Normalize()
	if err != nil {
		return value.Coefficient, int(value.Scale), 1
	}
	return normalized.Coefficient, int(normalized.Scale), 1
}

func moneyProjection(value *economics.Money) (int64, string, int) {
	if value == nil {
		return 0, "", 0
	}
	return value.NanoUnits, value.Currency, boolInt(value.Present)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func boolProjection(q bun.IDB, value int) any {
	if q.Dialect().Name() == dialect.PG {
		return value != 0
	}
	return value
}

func jsonArray(value any) (string, error) {
	if value == nil {
		return "[]", nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if string(b) == "null" {
		return "[]", nil
	}
	return string(b), nil
}

func insertValuationLine(ctx context.Context, q bun.IDB, storeID, valuationID string, valuationVersion uint32, line economics.LineItem) error {
	componentKey, componentHash := "", ""
	if line.Component != nil {
		componentKey, componentHash = line.Component.CanonicalKey(), line.Component.Fingerprint()
	}
	fixedFeeID, fixedFeeScope, fixedFeeVersion := "", "", ""
	if line.FixedFee != nil {
		fixedFeeID, fixedFeeScope, fixedFeeVersion = line.FixedFee.ID, string(line.FixedFee.Scope), line.FixedFee.Version
	}
	quantityCoefficient, quantityScale, quantityPresent := decimalProjection(line.Quantity)
	unitPriceCoefficient, unitPriceScale, unitPricePresent := decimalProjection(line.UnitPrice)
	numeratorCoefficient, numeratorScale, numeratorPresent := decimalProjection(line.RateNumerator)
	denominatorCoefficient, denominatorScale, denominatorPresent := decimalProjection(line.RateDenominator)
	amountCoefficient, amountScale, amountPresent := decimalProjection(line.Amount)
	roundedNano, roundedCurrency, roundedPresent := moneyProjection(line.RoundedAmount)
	sourceRefs, err := jsonArray(line.SourceObservationRefs)
	if err != nil {
		return fmt.Errorf("billingstore: line source refs JSON: %w", err)
	}
	adjustmentRefs, err := jsonArray(line.AdjustmentRefs)
	if err != nil {
		return fmt.Errorf("billingstore: line adjustment refs JSON: %w", err)
	}
	_, err = q.NewRaw(`
INSERT INTO billing_valuation_lines(
	store_id, valuation_id, valuation_version, line_id, rule_id, item_id, component_key, component_key_hash, fixed_fee_id, fixed_fee_scope, fixed_fee_version, unit,
	quantity_coefficient, quantity_scale, quantity_present, unit_price_coefficient, unit_price_scale, unit_price_present,
	rate_numerator_coefficient, rate_numerator_scale, rate_numerator_present, rate_denominator_coefficient, rate_denominator_scale, rate_denominator_present,
	amount_coefficient, amount_scale, amount_present, rounded_nano, rounded_currency, rounded_present, rounding_scope, rounding_policy, included_unit,
	source_observation_refs_json, adjustment_refs_json, projection_version
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`, storeID, valuationID, int64(valuationVersion), line.ID, line.RuleID, line.ItemID, componentKey, componentHash, fixedFeeID, fixedFeeScope, fixedFeeVersion, line.Unit,
		quantityCoefficient, quantityScale, boolProjection(q, quantityPresent), unitPriceCoefficient, unitPriceScale, boolProjection(q, unitPricePresent),
		numeratorCoefficient, numeratorScale, boolProjection(q, numeratorPresent), denominatorCoefficient, denominatorScale, boolProjection(q, denominatorPresent),
		amountCoefficient, amountScale, boolProjection(q, amountPresent), roundedNano, roundedCurrency, boolProjection(q, roundedPresent), string(line.RoundingScope), string(line.RoundingPolicy), boolProjection(q, boolInt(line.IncludedUnit)), sourceRefs, adjustmentRefs, BillingEconomicsProjectionVersion).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: insert valuation line: %w", err)
	}
	return nil
}

func (s *DurableStore) GetValuation(ctx context.Context, valuationID string, version uint32) (economics.Valuation, error) {
	if err := s.validateContext(ctx); err != nil {
		return economics.Valuation{}, err
	}
	if strings.TrimSpace(valuationID) == "" {
		return economics.Valuation{}, fmt.Errorf("billingstore: valuation id is required")
	}
	var payload string
	if err := s.db.NewRaw(`SELECT canonical_json FROM billing_valuations WHERE store_id = ? AND valuation_id = ? AND valuation_version = ? LIMIT 1`, s.storeID, valuationID, int64(version)).Scan(ctx, &payload); err != nil {
		return economics.Valuation{}, fmt.Errorf("billingstore: get valuation: %w", err)
	}
	var valuation economics.Valuation
	if err := json.Unmarshal([]byte(payload), &valuation); err != nil {
		return economics.Valuation{}, fmt.Errorf("billingstore: decode valuation: %w", err)
	}
	return valuation, nil
}

func (s *DurableStore) GetValuationVersion(ctx context.Context, valuationID string, version uint64) (economics.Valuation, error) {
	if err := s.validateContext(ctx); err != nil {
		return economics.Valuation{}, err
	}
	if version > math.MaxUint32 {
		return economics.Valuation{}, fmt.Errorf("billingstore: valuation version exceeds V2 range")
	}
	return s.GetValuation(ctx, valuationID, uint32(version))
}

func (s *DurableStore) ListValuations(ctx context.Context, query ValuationQuery) (ValuationPage, error) {
	if err := s.validateContext(ctx); err != nil {
		return ValuationPage{}, err
	}
	storeID, err := economicsStoreID(query.StoreID, s.storeID)
	if err != nil {
		return ValuationPage{}, err
	}
	kind, subject, err := economicsSubjectValues(query.SubjectKind, strings.TrimSpace(query.SubjectID), query.Subject)
	if err != nil {
		return ValuationPage{}, err
	}
	if query.Subject != nil && query.Subject.StoreID != storeID {
		return ValuationPage{}, fmt.Errorf("%w: subject store", ErrEconomicsOutOfScope)
	}
	tenantID := strings.TrimSpace(query.TenantID)
	if query.Subject != nil && query.Subject.TenantID != "" {
		if tenantID != "" && tenantID != query.Subject.TenantID {
			return ValuationPage{}, fmt.Errorf("%w: subject tenant", ErrEconomicsOutOfScope)
		}
		tenantID = query.Subject.TenantID
	}
	if query.Basis != "" {
		if err := query.Basis.Validate(); err != nil {
			return ValuationPage{}, fmt.Errorf("%w: basis", ErrEconomicsOutOfScope)
		}
	}
	if query.InputSetHash != "" {
		decoded, err := hex.DecodeString(query.InputSetHash)
		if err != nil || len(decoded) != sha256.Size {
			return ValuationPage{}, fmt.Errorf("%w: input set hash", ErrEconomicsOutOfScope)
		}
	}
	if kind == "" && tenantID == "" && query.Scope == "" && query.Basis == "" && query.InputSetHash == "" {
		return ValuationPage{}, ErrQueryTooBroad
	}
	limit, err := economicsLimit(query.Limit, 100)
	if err != nil {
		return ValuationPage{}, err
	}
	filter := struct{ StoreID, SubjectKind, SubjectID, TenantID, Scope, Basis, InputSetHash string }{storeID, string(kind), subject, tenantID, query.Scope, string(query.Basis), query.InputSetHash}
	hash := economicsFilterHash(filter)
	position, err := decodeEconomicsCursor(query.Cursor, "valuations", storeID, hash)
	if err != nil {
		return ValuationPage{}, err
	}
	where := []string{"store_id = ?"}
	args := []any{storeID}
	if kind != "" {
		where = append(where, "subject_kind = ?", "subject_id = ?")
		args = append(args, string(kind), subject)
	}
	if tenantID != "" {
		where = append(where, "tenant_id = ?")
		args = append(args, tenantID)
	}
	if query.Scope != "" {
		where = append(where, "scope = ?")
		args = append(args, query.Scope)
	}
	if query.Basis != "" {
		where = append(where, "basis = ?")
		args = append(args, string(query.Basis))
	}
	if query.InputSetHash != "" {
		where = append(where, "input_set_hash = ?")
		args = append(args, query.InputSetHash)
	}
	if query.Cursor != "" {
		where = append(where, `(created_at_unix > ? OR (created_at_unix = ? AND (valuation_id > ? OR (valuation_id = ? AND (valuation_version > ? OR (valuation_version = ? AND id > ?))))))`)
		args = append(args, position.CreatedAt, position.CreatedAt, position.RecordID, position.RecordID, position.RecordVersion, position.RecordVersion, position.RowID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT canonical_json, created_at_unix, valuation_id, valuation_version, id FROM billing_valuations WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at_unix ASC, valuation_id ASC, valuation_version ASC, id ASC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return ValuationPage{}, fmt.Errorf("billingstore: list valuations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type row struct {
		Payload                string
		CreatedAt, Version, ID int64
		ValuationID            string
	}
	items := make([]row, 0, limit+1)
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.Payload, &item.CreatedAt, &item.ValuationID, &item.Version, &item.ID); err != nil {
			return ValuationPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ValuationPage{}, err
	}
	page := ValuationPage{Valuations: make([]economics.Valuation, 0, minBillingInt(len(items), limit))}
	if len(items) > limit {
		last := items[limit-1]
		page.NextCursor = encodeEconomicsCursor(economicsCursor{Version: v2EconomicsCursorVersion, Kind: "valuations", StoreID: storeID, FilterHash: hash, CreatedAt: last.CreatedAt, RecordID: last.ValuationID, RecordVersion: last.Version, RowID: last.ID})
		items = items[:limit]
	}
	for _, item := range items {
		var valuation economics.Valuation
		if err := json.Unmarshal([]byte(item.Payload), &valuation); err != nil {
			return ValuationPage{}, fmt.Errorf("billingstore: decode listed valuation: %w", err)
		}
		page.Valuations = append(page.Valuations, valuation)
	}
	return page, nil
}

func (s *DurableStore) AppendReconciliation(ctx context.Context, reconciliation ReconciliationRecord) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: reconciliation begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.AppendReconciliationInTx(ctx, tx, reconciliation); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: reconciliation commit: %w", err)
	}
	return nil
}

func (s *DurableStore) AppendReconciliationInTx(ctx context.Context, tx bun.Tx, reconciliation ReconciliationRecord) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	canonical, payload, err := reconciliation.normalized()
	if err != nil {
		return err
	}
	if canonical.Subject.StoreID != s.storeID {
		return fmt.Errorf("%w: reconciliation subject store", ErrEconomicsOutOfScope)
	}
	if canonical.ResultSchemaVersion == ReconciliationRecordSchemaRetention {
		// Generic append must not persist a schema-2 payload that the
		// specialized retention reader would reject; route full results through
		// the authoritative retention validator and bind the outer envelope to
		// the parsed inner result before replay lookup or insertion.
		result, err := billing.ParseReconciliationRetentionResult(canonical.ResultJSON)
		if err != nil {
			return fmt.Errorf("%w: retention result payload: %w", ErrInvalidReconciliation, err)
		}
		if err := bindReconciliationRetentionEnvelope(canonical, result); err != nil {
			return fmt.Errorf("%w: retention envelope: %w", ErrInvalidReconciliation, err)
		}
	}
	if canonical.Version > math.MaxInt64 {
		return fmt.Errorf("billingstore: reconciliation version exceeds database range")
	}
	fingerprint := canonical.Fingerprint()
	var existing reconciliationRow
	if err := tx.NewRaw(reconciliationReplaySelect+` WHERE store_id = ? AND reconciliation_id = ? AND reconciliation_version = ? LIMIT 1`, s.storeID, canonical.ID, int64(canonical.Version)).Scan(ctx, &existing); err == nil {
		return resolveReconciliationReplay(existing, payload, canonical)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("billingstore: reconciliation identity lookup: %w", err)
	}
	subjectJSON, err := json.Marshal(canonical.Subject)
	if err != nil {
		return fmt.Errorf("billingstore: reconciliation subject JSON: %w", err)
	}
	if _, err := tx.NewRaw(`INSERT INTO billing_reconciliations(store_id, reconciliation_id, reconciliation_version, subject_kind, subject_id, subject_json, tenant_id, scope, basis, result_schema_version, input_set_hash, local_input_hash, provider_input_hash, policy_id, policy_version, result_json, canonical_json, fingerprint, projection_version, created_at_unix) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, s.storeID, canonical.ID, int64(canonical.Version), string(canonical.Subject.Kind), subjectIDForEconomics(canonical.Subject), string(subjectJSON), canonical.Subject.TenantID, canonical.Scope, string(canonical.Basis), canonical.ResultSchemaVersion, canonical.InputSetHash, canonical.LocalInputHash, canonical.ProviderInputHash, canonical.PolicyID, canonical.PolicyVersion, string(canonical.ResultJSON), string(payload), fingerprint, BillingEconomicsProjectionVersion, canonical.CreatedAt.UnixNano()).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: insert reconciliation: %w", err)
	}
	if err := tx.NewRaw(reconciliationReplaySelect+` WHERE store_id = ? AND reconciliation_id = ? AND reconciliation_version = ? LIMIT 1`, s.storeID, canonical.ID, int64(canonical.Version)).Scan(ctx, &existing); err != nil {
		return fmt.Errorf("billingstore: reconciliation row lookup after insert: %w", err)
	}
	if err := resolveReconciliationReplay(existing, payload, canonical); err != nil {
		return err
	}
	if err := s.economicFault("after_reconciliation"); err != nil {
		return err
	}
	return nil
}

// reconciliationReplaySelect carries the durable generation discriminator and
// every schema-2 projection identity column so a replay lookup can validate an
// existing row before any identity comparison.
const reconciliationReplaySelect = `SELECT id, reconciliation_id, reconciliation_version, canonical_json, result_json, fingerprint, result_schema_version, projection_version, subject_kind, subject_id, subject_json, tenant_id, scope, basis, input_set_hash, local_input_hash, provider_input_hash, policy_id, policy_version, created_at_unix FROM billing_reconciliations`

type reconciliationRow struct {
	ID                    int64  `bun:"id"`
	ReconciliationID      string `bun:"reconciliation_id"`
	ReconciliationVersion int64  `bun:"reconciliation_version"`
	CanonicalJSON         string `bun:"canonical_json"`
	ResultJSON            string `bun:"result_json"`
	Fingerprint           string `bun:"fingerprint"`
	ResultSchemaVersion   int64  `bun:"result_schema_version"`
	ProjectionVersion     int64  `bun:"projection_version"`
	SubjectKind           string `bun:"subject_kind"`
	SubjectID             string `bun:"subject_id"`
	SubjectJSON           string `bun:"subject_json"`
	TenantID              string `bun:"tenant_id"`
	Scope                 string `bun:"scope"`
	Basis                 string `bun:"basis"`
	InputSetHash          string `bun:"input_set_hash"`
	LocalInputHash        string `bun:"local_input_hash"`
	ProviderInputHash     string `bun:"provider_input_hash"`
	PolicyID              string `bun:"policy_id"`
	PolicyVersion         string `bun:"policy_version"`
	CreatedAt             int64  `bun:"created_at_unix"`
}

func (r reconciliationRow) storedProjection() reconciliationStoredProjection {
	return reconciliationStoredProjection{
		SubjectKind: r.SubjectKind, SubjectID: r.SubjectID, SubjectJSON: r.SubjectJSON,
		TenantID: r.TenantID, Scope: r.Scope, Basis: r.Basis,
		ResultSchemaVersion: r.ResultSchemaVersion,
		InputSetHash:        r.InputSetHash, LocalInputHash: r.LocalInputHash, ProviderInputHash: r.ProviderInputHash,
		PolicyID: r.PolicyID, PolicyVersion: r.PolicyVersion, CreatedAt: r.CreatedAt,
		ProjectionVersion: r.ProjectionVersion,
	}
}

// resolveReconciliationReplay validates an existing row before comparing its
// identity with the incoming canonical record. The stored canonical envelope is
// decoded and must carry the durable generation; a schema-2 row must also bind
// its full projection identity. Schema 2 never falls back to the result
// projection or to an empty fingerprint, and an unchanged canonical row must
// match the incoming canonical bytes exactly.
func resolveReconciliationReplay(existing reconciliationRow, payload []byte, reconciliation ReconciliationRecord) error {
	conflict := func(inner error, format string, args ...any) error {
		detail := fmt.Sprintf(format, args...)
		if inner != nil {
			return fmt.Errorf("%w: reconciliation_id=%q version=%d: %s: %w", ErrIdentityConflict, reconciliation.ID, reconciliation.Version, detail, inner)
		}
		return fmt.Errorf("%w: reconciliation_id=%q version=%d: %s", ErrIdentityConflict, reconciliation.ID, reconciliation.Version, detail)
	}
	durableGeneration, err := durableReconciliationGeneration(existing.ResultSchemaVersion)
	if err != nil {
		return conflict(err, "stored generation is invalid")
	}
	if existing.CanonicalJSON == "" {
		// Rows created by the additive migration before canonical_json was
		// introduced remain replay-readable through their result projection.
		// That compatibility path exists only for the explicitly legacy
		// generation; schema 2 is never synthesized from projections.
		if durableGeneration != ReconciliationRecordSchemaLegacy || reconciliation.ResultSchemaVersion != ReconciliationRecordSchemaLegacy {
			return conflict(nil, "canonical bytes are absent for generation %d", durableGeneration)
		}
		if existing.ResultJSON != string(reconciliation.ResultJSON) {
			return conflict(nil, "stored result projection differs")
		}
		if existing.Fingerprint != "" && existing.Fingerprint != reconciliation.Fingerprint() {
			return conflict(nil, "stored fingerprint differs")
		}
		return nil
	}
	stored, _, err := decodeCanonicalReconciliation([]byte(existing.CanonicalJSON))
	if err != nil {
		return conflict(err, "stored canonical JSON is invalid")
	}
	if stored.ResultSchemaVersion != durableGeneration {
		return conflict(nil, "canonical generation %d does not match durable generation %d", stored.ResultSchemaVersion, durableGeneration)
	}
	if durableGeneration == ReconciliationRecordSchemaRetention {
		if err := bindReconciliationStoredProjection(stored, existing.storedProjection()); err != nil {
			return conflict(err, "stored projection is invalid")
		}
	}
	if durableGeneration != reconciliation.ResultSchemaVersion {
		return conflict(nil, "durable generation %d does not match incoming generation %d", durableGeneration, reconciliation.ResultSchemaVersion)
	}
	if existing.CanonicalJSON != string(payload) {
		return conflict(nil, "stored canonical bytes differ")
	}
	if existing.Fingerprint == "" {
		if durableGeneration == ReconciliationRecordSchemaRetention {
			return conflict(nil, "schema-2 replay requires a stored fingerprint")
		}
		return nil
	}
	if existing.Fingerprint != reconciliation.Fingerprint() {
		return conflict(nil, "stored fingerprint differs")
	}
	return nil
}

func (s *DurableStore) GetReconciliation(ctx context.Context, reconciliationID string, version uint64) (ReconciliationRecord, error) {
	if err := s.validateContext(ctx); err != nil {
		return ReconciliationRecord{}, err
	}
	if strings.TrimSpace(reconciliationID) == "" {
		return ReconciliationRecord{}, fmt.Errorf("billingstore: reconciliation id is required")
	}
	if version > math.MaxInt64 {
		return ReconciliationRecord{}, fmt.Errorf("billingstore: reconciliation version exceeds database range")
	}
	var row struct {
		ResultJSON          string `bun:"result_json"`
		ID                  string `bun:"reconciliation_id"`
		Version             int64  `bun:"reconciliation_version"`
		SubjectKind         string `bun:"subject_kind"`
		SubjectID           string `bun:"subject_id"`
		SubjectJSON         string `bun:"subject_json"`
		CanonicalJSON       string `bun:"canonical_json"`
		TenantID            string `bun:"tenant_id"`
		Scope               string `bun:"scope"`
		Basis               string `bun:"basis"`
		ResultSchemaVersion int64  `bun:"result_schema_version"`
		InputSetHash        string `bun:"input_set_hash"`
		LocalInputHash      string `bun:"local_input_hash"`
		ProviderInputHash   string `bun:"provider_input_hash"`
		PolicyID            string `bun:"policy_id"`
		PolicyVersion       string `bun:"policy_version"`
		ProjectionVersion   int64  `bun:"projection_version"`
		CreatedAt           int64  `bun:"created_at_unix"`
	}
	if err := s.db.NewRaw(`SELECT reconciliation_id, reconciliation_version, subject_kind, subject_id, subject_json, canonical_json, tenant_id, scope, basis, result_schema_version, input_set_hash, local_input_hash, provider_input_hash, policy_id, policy_version, result_json, projection_version, created_at_unix FROM billing_reconciliations WHERE store_id = ? AND reconciliation_id = ? AND reconciliation_version = ? LIMIT 1`, s.storeID, reconciliationID, int64(version)).Scan(ctx, &row); err != nil {
		return ReconciliationRecord{}, fmt.Errorf("billingstore: get reconciliation: %w", err)
	}
	if row.CanonicalJSON != "" {
		record, _, err := decodeCanonicalReconciliation([]byte(row.CanonicalJSON))
		if err != nil {
			return ReconciliationRecord{}, fmt.Errorf("billingstore: decode reconciliation canonical JSON: %w", err)
		}
		// The decoded wire generation must equal the durable discriminator
		// before the stored value can be assigned or a projection bound.
		generation, err := bindReconciliationReadGeneration(record.ResultSchemaVersion, row.ResultSchemaVersion)
		if err != nil {
			return ReconciliationRecord{}, fmt.Errorf("billingstore: reconciliation generation: %w", err)
		}
		record.ResultSchemaVersion = generation
		if generation == ReconciliationRecordSchemaRetention {
			if err := bindReconciliationStoredProjection(record, reconciliationStoredProjection{
				SubjectKind: row.SubjectKind, SubjectID: row.SubjectID, SubjectJSON: row.SubjectJSON,
				TenantID: row.TenantID, Scope: row.Scope, Basis: row.Basis,
				ResultSchemaVersion: row.ResultSchemaVersion,
				InputSetHash:        row.InputSetHash, LocalInputHash: row.LocalInputHash, ProviderInputHash: row.ProviderInputHash,
				PolicyID: row.PolicyID, PolicyVersion: row.PolicyVersion, CreatedAt: row.CreatedAt,
				ProjectionVersion: row.ProjectionVersion,
			}); err != nil {
				return ReconciliationRecord{}, fmt.Errorf("billingstore: reconciliation projection: %w", err)
			}
		}
		return record, nil
	}
	// Canonical bytes are absent, so only the explicitly legacy generation is
	// addressable through projections; schema 2 is never synthesized.
	generation, err := durableReconciliationGeneration(row.ResultSchemaVersion)
	if err != nil {
		return ReconciliationRecord{}, fmt.Errorf("billingstore: reconciliation generation: %w", err)
	}
	if generation != ReconciliationRecordSchemaLegacy {
		return ReconciliationRecord{}, fmt.Errorf("%w: canonical bytes are absent for generation %d", ErrReconciliationRetentionMismatch, generation)
	}
	result := json.RawMessage(row.ResultJSON)
	subject := subjectFromProjection(metering.SubjectKind(row.SubjectKind), s.storeID, row.SubjectID, row.TenantID)
	if row.SubjectJSON != "" {
		if err := json.Unmarshal([]byte(row.SubjectJSON), &subject); err != nil {
			return ReconciliationRecord{}, fmt.Errorf("billingstore: decode reconciliation subject: %w", err)
		}
	}
	reconciliation := ReconciliationRecord{ID: row.ID, Version: uint64(row.Version), Subject: subject, Scope: row.Scope, Basis: economics.ValuationBasis(row.Basis), ResultSchemaVersion: generation, InputSetHash: row.InputSetHash, LocalInputHash: row.LocalInputHash, ProviderInputHash: row.ProviderInputHash, PolicyID: row.PolicyID, PolicyVersion: row.PolicyVersion, ResultJSON: result, Result: append(json.RawMessage(nil), result...), CreatedAt: time.Unix(0, row.CreatedAt).UTC()}
	return reconciliation, nil
}

func subjectFromProjection(kind metering.SubjectKind, storeID, id, tenant string) metering.SubjectRef {
	subject := metering.SubjectRef{Kind: kind, StoreID: storeID, TenantID: tenant}
	switch kind {
	case metering.SubjectALeg:
		subject.ALegID = id
	case metering.SubjectRequest:
		subject.RequestID = id
	case metering.SubjectBillingCall:
		subject.BillingCallID = id
	case metering.SubjectBLeg:
		subject.BLegID = id
	case metering.SubjectSubmission:
		subject.SubmissionID = id
	case metering.SubjectProviderCharge:
		subject.ProviderChargeID = id
		subject.ProviderAccountKey = id
	case metering.SubjectProviderDebit:
		subject.BLegID = id
	case metering.SubjectResource:
		subject.ResourceID = id
		subject.PeriodID = id
	case metering.SubjectAccountWindow:
		subject.WindowID = id
		subject.ProviderAccountKey = id
	case metering.SubjectStatementLine:
		subject.StatementLineID = id
		subject.StatementID = id
		subject.ProviderAccountKey = id
	}
	return subject
}

func (s *DurableStore) ListReconciliations(ctx context.Context, query ReconciliationQuery) (ReconciliationPage, error) {
	if err := s.validateContext(ctx); err != nil {
		return ReconciliationPage{}, err
	}
	storeID, err := economicsStoreID(query.StoreID, s.storeID)
	if err != nil {
		return ReconciliationPage{}, err
	}
	kind, subject, err := economicsSubjectValues(query.SubjectKind, strings.TrimSpace(query.SubjectID), query.Subject)
	if err != nil {
		return ReconciliationPage{}, err
	}
	if query.Subject != nil && query.Subject.StoreID != storeID {
		return ReconciliationPage{}, fmt.Errorf("%w: subject store", ErrEconomicsOutOfScope)
	}
	tenantID := strings.TrimSpace(query.TenantID)
	if query.Subject != nil && query.Subject.TenantID != "" {
		if tenantID != "" && tenantID != query.Subject.TenantID {
			return ReconciliationPage{}, fmt.Errorf("%w: subject tenant", ErrEconomicsOutOfScope)
		}
		tenantID = query.Subject.TenantID
	}
	if query.Basis != "" {
		if err := query.Basis.Validate(); err != nil {
			return ReconciliationPage{}, fmt.Errorf("%w: basis", ErrEconomicsOutOfScope)
		}
	}
	if query.InputSetHash != "" {
		decoded, err := hex.DecodeString(query.InputSetHash)
		if err != nil || len(decoded) != sha256.Size {
			return ReconciliationPage{}, fmt.Errorf("%w: input set hash", ErrEconomicsOutOfScope)
		}
	}
	if kind == "" && tenantID == "" && query.Scope == "" && query.Basis == "" && query.InputSetHash == "" {
		return ReconciliationPage{}, ErrQueryTooBroad
	}
	limit, err := economicsLimit(query.Limit, 100)
	if err != nil {
		return ReconciliationPage{}, err
	}
	filter := struct{ StoreID, SubjectKind, SubjectID, TenantID, Scope, Basis, InputSetHash string }{storeID, string(kind), subject, tenantID, query.Scope, string(query.Basis), query.InputSetHash}
	hash := economicsFilterHash(filter)
	position, err := decodeEconomicsCursor(query.Cursor, "reconciliations", storeID, hash)
	if err != nil {
		return ReconciliationPage{}, err
	}
	where := []string{"store_id = ?"}
	args := []any{storeID}
	if kind != "" {
		where = append(where, "subject_kind = ?", "subject_id = ?")
		args = append(args, string(kind), subject)
	}
	if tenantID != "" {
		where = append(where, "tenant_id = ?")
		args = append(args, tenantID)
	}
	if query.Scope != "" {
		where = append(where, "scope = ?")
		args = append(args, query.Scope)
	}
	if query.Basis != "" {
		where = append(where, "basis = ?")
		args = append(args, string(query.Basis))
	}
	if query.InputSetHash != "" {
		where = append(where, "input_set_hash = ?")
		args = append(args, query.InputSetHash)
	}
	if query.Cursor != "" {
		where = append(where, `(created_at_unix > ? OR (created_at_unix = ? AND (reconciliation_id > ? OR (reconciliation_id = ? AND (reconciliation_version > ? OR (reconciliation_version = ? AND id > ?))))))`)
		args = append(args, position.CreatedAt, position.CreatedAt, position.RecordID, position.RecordID, position.RecordVersion, position.RecordVersion, position.RowID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT reconciliation_id, reconciliation_version, subject_kind, subject_id, subject_json, canonical_json, tenant_id, scope, basis, result_schema_version, input_set_hash, local_input_hash, provider_input_hash, policy_id, policy_version, result_json, projection_version, created_at_unix, id FROM billing_reconciliations WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at_unix ASC, reconciliation_id ASC, reconciliation_version ASC, id ASC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return ReconciliationPage{}, fmt.Errorf("billingstore: list reconciliations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type row struct {
		ID                                                                                                                                                               string
		Version, CreatedAt, RowID, ProjectionVersion                                                                                                                     int64
		SubjectKind, SubjectID, SubjectJSON, CanonicalJSON, TenantID, Scope, Basis, InputSetHash, LocalInputHash, ProviderInputHash, PolicyID, PolicyVersion, ResultJSON string
		ResultSchemaVersion                                                                                                                                              int64
	}
	items := make([]row, 0, limit+1)
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.ID, &item.Version, &item.SubjectKind, &item.SubjectID, &item.SubjectJSON, &item.CanonicalJSON, &item.TenantID, &item.Scope, &item.Basis, &item.ResultSchemaVersion, &item.InputSetHash, &item.LocalInputHash, &item.ProviderInputHash, &item.PolicyID, &item.PolicyVersion, &item.ResultJSON, &item.ProjectionVersion, &item.CreatedAt, &item.RowID); err != nil {
			return ReconciliationPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ReconciliationPage{}, err
	}
	page := ReconciliationPage{Reconciliations: make([]ReconciliationRecord, 0, minBillingInt(len(items), limit))}
	if len(items) > limit {
		last := items[limit-1]
		page.NextCursor = encodeEconomicsCursor(economicsCursor{Version: v2EconomicsCursorVersion, Kind: "reconciliations", StoreID: storeID, FilterHash: hash, CreatedAt: last.CreatedAt, RecordID: last.ID, RecordVersion: last.Version, RowID: last.RowID})
		items = items[:limit]
	}
	for _, item := range items {
		if item.CanonicalJSON != "" {
			record, _, err := decodeCanonicalReconciliation([]byte(item.CanonicalJSON))
			if err != nil {
				return ReconciliationPage{}, fmt.Errorf("billingstore: decode listed reconciliation canonical JSON: %w", err)
			}
			// The decoded wire generation must equal the durable discriminator
			// before the stored value can be assigned or a projection bound.
			generation, err := bindReconciliationReadGeneration(record.ResultSchemaVersion, item.ResultSchemaVersion)
			if err != nil {
				return ReconciliationPage{}, fmt.Errorf("billingstore: listed reconciliation generation: %w", err)
			}
			record.ResultSchemaVersion = generation
			if generation == ReconciliationRecordSchemaRetention {
				if err := bindReconciliationStoredProjection(record, reconciliationStoredProjection{
					SubjectKind: item.SubjectKind, SubjectID: item.SubjectID, SubjectJSON: item.SubjectJSON,
					TenantID: item.TenantID, Scope: item.Scope, Basis: item.Basis,
					ResultSchemaVersion: item.ResultSchemaVersion,
					InputSetHash:        item.InputSetHash, LocalInputHash: item.LocalInputHash, ProviderInputHash: item.ProviderInputHash,
					PolicyID: item.PolicyID, PolicyVersion: item.PolicyVersion, CreatedAt: item.CreatedAt,
					ProjectionVersion: item.ProjectionVersion,
				}); err != nil {
					return ReconciliationPage{}, fmt.Errorf("billingstore: listed reconciliation projection: %w", err)
				}
			}
			page.Reconciliations = append(page.Reconciliations, record)
			continue
		}
		// Canonical bytes are absent, so only the explicitly legacy generation
		// is addressable through projections; schema 2 is never synthesized.
		generation, err := durableReconciliationGeneration(item.ResultSchemaVersion)
		if err != nil {
			return ReconciliationPage{}, fmt.Errorf("billingstore: listed reconciliation generation: %w", err)
		}
		if generation != ReconciliationRecordSchemaLegacy {
			return ReconciliationPage{}, fmt.Errorf("%w: canonical bytes are absent for generation %d", ErrReconciliationRetentionMismatch, generation)
		}
		result := json.RawMessage(item.ResultJSON)
		subject := subjectFromProjection(metering.SubjectKind(item.SubjectKind), storeID, item.SubjectID, item.TenantID)
		if item.SubjectJSON != "" {
			if err := json.Unmarshal([]byte(item.SubjectJSON), &subject); err != nil {
				return ReconciliationPage{}, fmt.Errorf("billingstore: decode listed reconciliation subject: %w", err)
			}
		}
		page.Reconciliations = append(page.Reconciliations, ReconciliationRecord{ID: item.ID, Version: uint64(item.Version), Subject: subject, Scope: item.Scope, Basis: economics.ValuationBasis(item.Basis), ResultSchemaVersion: generation, InputSetHash: item.InputSetHash, LocalInputHash: item.LocalInputHash, ProviderInputHash: item.ProviderInputHash, PolicyID: item.PolicyID, PolicyVersion: item.PolicyVersion, ResultJSON: result, Result: append(json.RawMessage(nil), result...), CreatedAt: time.Unix(0, item.CreatedAt).UTC()})
	}
	return page, nil
}

func (s *DurableStore) AppendEconomicWorkInTx(ctx context.Context, tx bun.Tx, marker EconomicWorkMarker) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	canonical, payload, err := marker.normalized()
	if err != nil {
		return err
	}
	if canonical.InputSetHash != "" && len(canonical.InputSetHash) != sha256.Size*2 {
		return fmt.Errorf("%w: input set hash", ErrInvalidEconomicWork)
	}
	if canonical.Version > math.MaxInt64 {
		return fmt.Errorf("%w: version exceeds database range", ErrInvalidEconomicWork)
	}
	fingerprintBytes := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(fingerprintBytes[:])
	var existing struct {
		PayloadJSON string `bun:"payload_json"`
		Fingerprint string `bun:"fingerprint"`
	}
	if err := tx.NewRaw(`SELECT payload_json, fingerprint FROM billing_economic_work WHERE store_id = ? AND work_id = ? AND work_version = ? LIMIT 1`, s.storeID, canonical.ID, int64(canonical.Version)).Scan(ctx, &existing); err == nil {
		if existing.PayloadJSON == string(payload) && existing.Fingerprint == fingerprint {
			return nil
		}
		return fmt.Errorf("%w: work_id=%q version=%d", ErrIdentityConflict, canonical.ID, canonical.Version)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("billingstore: work identity lookup: %w", err)
	}
	if _, err := tx.NewRaw(`INSERT INTO billing_economic_work(store_id, work_id, work_version, kind, subject_kind, subject_id, input_set_hash, payload_json, fingerprint, status, created_at_unix) VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, s.storeID, canonical.ID, int64(canonical.Version), canonical.Kind, string(canonical.SubjectKind), canonical.SubjectID, canonical.InputSetHash, string(payload), fingerprint, canonical.Status, canonical.CreatedAt.UnixNano()).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: insert economic work: %w", err)
	}
	if err := tx.NewRaw(`SELECT payload_json, fingerprint FROM billing_economic_work WHERE store_id = ? AND work_id = ? AND work_version = ? LIMIT 1`, s.storeID, canonical.ID, int64(canonical.Version)).Scan(ctx, &existing); err != nil {
		return fmt.Errorf("billingstore: economic work row lookup after insert: %w", err)
	}
	if existing.PayloadJSON != string(payload) || existing.Fingerprint != fingerprint {
		return fmt.Errorf("%w: work_id=%q version=%d", ErrIdentityConflict, canonical.ID, canonical.Version)
	}
	if err := s.economicFault("after_work"); err != nil {
		return err
	}
	return nil
}

func (s *DurableStore) AppendEconomicWork(ctx context.Context, marker EconomicWorkMarker) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.AppendEconomicWorkInTx(ctx, tx, marker); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *DurableStore) SetEconomicFaultHook(hook func(string) error) {
	if s != nil {
		s.economicFaultHook = hook
	}
}
func (s *DurableStore) economicFault(stage string) error {
	if s == nil || s.economicFaultHook == nil {
		return nil
	}
	if err := s.economicFaultHook(stage); err != nil {
		return fmt.Errorf("billingstore: economic failpoint %s: %w", stage, err)
	}
	return nil
}

// AppendObservationEconomics composes one observation, valuation,
// reconciliation and optional work marker on one local transaction. The
// journal writer is intentionally a narrow infra interface.
func (s *DurableStore) AppendObservationEconomics(ctx context.Context, writer journalstore.ObservationTxWriter, observation metering.Observation, valuation *economics.Valuation, reconciliation *ReconciliationRecord, marker *EconomicWorkMarker) error {
	return s.appendObservationEconomics(ctx, writer, observation, valuation, reconciliation, marker)
}

func (s *DurableStore) appendObservationEconomics(ctx context.Context, writer journalstore.ObservationTxWriter, observation metering.Observation, valuation *economics.Valuation, reconciliation *ReconciliationRecord, marker *EconomicWorkMarker) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	if writer == nil {
		return fmt.Errorf("billingstore: nil observation writer")
	}
	if observation.Subject.StoreID != s.storeID || observation.Correlation.StoreID != s.storeID {
		return fmt.Errorf("%w: observation store", ErrEconomicsOutOfScope)
	}
	if dbWriter, ok := writer.(interface{ DB() *bun.DB }); ok && dbWriter.DB() != s.db {
		return ErrCrossDatabaseTransaction
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: economics begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := writer.AppendObservationInTx(ctx, tx, observation); err != nil {
		return err
	}
	if err := s.economicFault("after_observation"); err != nil {
		return err
	}
	if valuation != nil {
		if err := s.AppendValuationInTx(ctx, tx, *valuation); err != nil {
			return err
		}
	}
	if reconciliation != nil {
		if err := s.AppendReconciliationInTx(ctx, tx, *reconciliation); err != nil {
			return err
		}
	}
	if marker != nil {
		if err := s.AppendEconomicWorkInTx(ctx, tx, *marker); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: economics commit: %w", err)
	}
	return nil
}

// AppendObservationBundle is a value-oriented composition spelling for
// callers that always provide all four records.
func (s *DurableStore) AppendObservationBundle(ctx context.Context, writer journalstore.ObservationTxWriter, observation metering.Observation, valuation economics.Valuation, reconciliation ReconciliationRecord, marker EconomicWorkMarker) error {
	return s.appendObservationEconomics(ctx, writer, observation, &valuation, &reconciliation, &marker)
}

// AppendObservationWithEconomics is an optional-record composition spelling.
func (s *DurableStore) AppendObservationWithEconomics(ctx context.Context, writer journalstore.ObservationTxWriter, observation metering.Observation, valuation *economics.Valuation, reconciliation *ReconciliationRecord, marker *EconomicWorkMarker) error {
	return s.appendObservationEconomics(ctx, writer, observation, valuation, reconciliation, marker)
}

// RebuildValuationProjections recreates all line projections from immutable
// canonical valuation JSON and repairs projection-version drift.
func (s *DurableStore) RebuildValuationProjections(ctx context.Context) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: rebuild begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.NewRaw(`DELETE FROM billing_valuation_lines WHERE store_id = ?`, s.storeID).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: rebuild delete lines: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT valuation_id, valuation_version, input_set_hash, canonical_json, fingerprint FROM billing_valuations WHERE store_id = ? ORDER BY id ASC`, s.storeID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, inputSetHash, payload, fingerprint string
		var version int64
		if err := rows.Scan(&id, &version, &inputSetHash, &payload, &fingerprint); err != nil {
			return err
		}
		var valuation economics.Valuation
		if err := json.Unmarshal([]byte(payload), &valuation); err != nil {
			return fmt.Errorf("billingstore: rebuild decode valuation: %w", err)
		}
		var canonical economics.Valuation
		var canonicalJSON []byte
		if inputSetHash == "" && valuation.InputSetHash == "" {
			canonical, canonicalJSON, err = canonicalLegacyValuationForStore(s.storeID, valuation)
		} else {
			canonical, canonicalJSON, err = canonicalValuationForStore(s.storeID, valuation)
		}
		if err != nil {
			return err
		}
		if string(canonicalJSON) != payload || (fingerprint != "" && fingerprint != canonical.Fingerprint()) {
			return fmt.Errorf("%w: valuation payload drift", ErrIdentityConflict)
		}
		if err := insertValuationLines(ctx, tx, s.storeID, canonical); err != nil {
			return err
		}
		if _, err := tx.NewRaw(`UPDATE billing_valuations SET projection_version = ? WHERE store_id = ? AND valuation_id = ? AND valuation_version = ?`, BillingEconomicsProjectionVersion, s.storeID, id, version).Exec(ctx); err != nil {
			return fmt.Errorf("billingstore: rebuild valuation version: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("billingstore: rebuild valuation rows close: %w", err)
	}
	if err := rebuildReconciliationProjections(ctx, tx, s.storeID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: rebuild commit: %w", err)
	}
	return nil
}

func rebuildReconciliationProjections(ctx context.Context, tx bun.Tx, storeID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT reconciliation_id, reconciliation_version, canonical_json, fingerprint, result_schema_version FROM billing_reconciliations WHERE store_id = ? AND canonical_json != '' ORDER BY id ASC`, storeID)
	if err != nil {
		return fmt.Errorf("billingstore: rebuild reconciliation scan: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, payload, fingerprint string
		var version, schemaVersion int64
		if err := rows.Scan(&id, &version, &payload, &fingerprint, &schemaVersion); err != nil {
			return fmt.Errorf("billingstore: rebuild reconciliation row: %w", err)
		}
		record, canonicalJSON, err := decodeCanonicalReconciliation([]byte(payload))
		if err != nil {
			return fmt.Errorf("billingstore: rebuild decode reconciliation: %w", err)
		}
		if _, err := bindReconciliationReadGeneration(record.ResultSchemaVersion, schemaVersion); err != nil {
			return fmt.Errorf("billingstore: rebuild reconciliation generation: %w", err)
		}
		if string(canonicalJSON) != payload || (fingerprint != "" && fingerprint != record.Fingerprint()) {
			return fmt.Errorf("%w: reconciliation payload drift", ErrIdentityConflict)
		}
		subjectJSON, err := json.Marshal(record.Subject)
		if err != nil {
			return fmt.Errorf("billingstore: rebuild reconciliation subject JSON: %w", err)
		}
		if _, err := tx.NewRaw(`
UPDATE billing_reconciliations SET
	subject_kind = ?, subject_id = ?, subject_json = ?, tenant_id = ?, scope = ?, basis = ?,
	input_set_hash = ?, local_input_hash = ?, provider_input_hash = ?, policy_id = ?, policy_version = ?,
	result_json = ?, projection_version = ?
WHERE store_id = ? AND reconciliation_id = ? AND reconciliation_version = ?`,
			string(record.Subject.Kind), subjectIDForEconomics(record.Subject), string(subjectJSON), record.Subject.TenantID, record.Scope, string(record.Basis),
			record.InputSetHash, record.LocalInputHash, record.ProviderInputHash, record.PolicyID, record.PolicyVersion,
			string(record.ResultJSON), BillingEconomicsProjectionVersion, storeID, id, version).Exec(ctx); err != nil {
			return fmt.Errorf("billingstore: rebuild reconciliation projection: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("billingstore: rebuild reconciliation rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("billingstore: rebuild reconciliation rows close: %w", err)
	}
	return nil
}

func (s *DurableStore) RebuildProjections(ctx context.Context) error {
	return s.RebuildValuationProjections(ctx)
}
func (s *DurableStore) RebuildEconomicProjections(ctx context.Context) error {
	return s.RebuildValuationProjections(ctx)
}

func minBillingInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
