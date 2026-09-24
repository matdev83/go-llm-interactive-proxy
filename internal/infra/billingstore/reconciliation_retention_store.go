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
)

// ErrReconciliationRetentionMismatch identifies a durable reconciliation that
// is not a full-result retention record, or whose stored bytes are not the
// canonical result.
var ErrReconciliationRetentionMismatch = errors.New("billingstore: reconciliation retention record mismatch")

// ErrReconciliationRetentionQuery identifies a malformed latest-retention
// query identity. Query identities are validated before any SQL runs.
var ErrReconciliationRetentionQuery = errors.New("billingstore: invalid reconciliation retention query")

// ReconciliationRetentionQuery selects the newest durable full-result
// reconciliation for one explicitly subject-scoped economic scope. The subject
// kind and id are required and bounded; the optional tenant refines the same
// subject partition. Scope- or input-set-hash-only predicates are deliberately
// unsupported because the retention index is subject-leading and those shapes
// could scan an unbounded number of rows for a single result.
type ReconciliationRetentionQuery struct {
	SubjectKind metering.SubjectKind
	SubjectID   string
	TenantID    string
}

// latestRetentionSelect is the shared projection for the supported latest
// query shapes. Both shapes filter on the retention index prefix
// (store_id, result_schema_version, subject_kind, subject_id) and order by the
// deterministic total tie-break, so LIMIT 1 stops with bounded index work.
const latestRetentionSelect = `SELECT reconciliation_id, reconciliation_version FROM billing_reconciliations`

const latestRetentionSubjectSQL = latestRetentionSelect + ` WHERE store_id = ? AND result_schema_version = ? AND subject_kind = ? AND subject_id = ? ORDER BY created_at_unix DESC, reconciliation_id DESC, reconciliation_version DESC, id DESC LIMIT 1`

const latestRetentionSubjectTenantSQL = latestRetentionSelect + ` WHERE store_id = ? AND result_schema_version = ? AND subject_kind = ? AND subject_id = ? AND tenant_id = ? ORDER BY created_at_unix DESC, reconciliation_id DESC, reconciliation_version DESC, id DESC LIMIT 1`

// ReconciliationRecordFromRetention maps the canonical full result into the
// durable reconciliation envelope. It does not write anything.
func ReconciliationRecordFromRetention(result billing.ReconciliationRetentionResult) (ReconciliationRecord, error) {
	canonical, err := result.CanonicalJSON()
	if err != nil {
		return ReconciliationRecord{}, err
	}
	return ReconciliationRecord{
		ID:                  result.ID,
		Version:             result.ResultRevision,
		Subject:             result.Subject,
		Scope:               result.Scope,
		ResultSchemaVersion: ReconciliationRecordSchemaRetention,
		InputSetHash:        result.InputSetHash,
		LocalInputHash:      result.LocalInputHash,
		ProviderInputHash:   result.ProviderInputHash,
		PolicyID:            result.Policy.ID,
		PolicyVersion:       result.Policy.Version,
		ResultJSON:          canonical,
		CreatedAt:           result.CreatedAt,
	}, nil
}

// AppendReconciliationRetention durably appends one immutable full-result
// reconciliation in its own local transaction.
func (s *DurableStore) AppendReconciliationRetention(ctx context.Context, result billing.ReconciliationRetentionResult) error {
	record, err := ReconciliationRecordFromRetention(result)
	if err != nil {
		return err
	}
	return s.AppendReconciliation(ctx, record)
}

// AppendReconciliationRetentionInTx appends the full result through an
// existing local transaction so callers can compose it atomically.
func (s *DurableStore) AppendReconciliationRetentionInTx(ctx context.Context, tx bun.Tx, result billing.ReconciliationRetentionResult) error {
	record, err := ReconciliationRecordFromRetention(result)
	if err != nil {
		return err
	}
	return s.AppendReconciliationInTx(ctx, tx, record)
}

// GetReconciliationRetention reads one immutable retention result and verifies
// its canonical bytes. A legacy single-basis reconciliation id is rejected.
func (s *DurableStore) GetReconciliationRetention(ctx context.Context, reconciliationID string, revision uint64) (billing.ReconciliationRetentionResult, error) {
	record, err := s.GetReconciliation(ctx, reconciliationID, revision)
	if err != nil {
		return billing.ReconciliationRetentionResult{}, err
	}
	return reconciliationRetentionFromRecord(record)
}

// LatestReconciliationRetention returns the newest retention revision for the
// required subject scope with a deterministic total order (created_at, id,
// revision, row id). The subject identity and optional tenant are validated
// against the domain subject bounds before SQL, and both supported query
// shapes are served by the retention index prefix, so LIMIT 1 performs bounded
// index work instead of scanning unrelated rows.
func (s *DurableStore) LatestReconciliationRetention(ctx context.Context, query ReconciliationRetentionQuery) (billing.ReconciliationRetentionResult, bool, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.ReconciliationRetentionResult{}, false, err
	}
	if query.SubjectKind == "" || query.SubjectID == "" {
		// Without an explicit subject there is no bounded economic scope.
		return billing.ReconciliationRetentionResult{}, false, ErrQueryTooBroad
	}
	if !query.SubjectKind.IsKnown() {
		return billing.ReconciliationRetentionResult{}, false, fmt.Errorf("%w: unknown subject kind %q", ErrReconciliationRetentionQuery, query.SubjectKind)
	}
	// Reuse the authoritative domain subject validator: a B-leg probe validates
	// the subject id and tenant with exactly the same bounded-identity rules the
	// stored subject fields were validated with, for every supported kind.
	probe := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: s.storeID, BLegID: query.SubjectID, TenantID: query.TenantID}
	if err := probe.Validate(); err != nil {
		return billing.ReconciliationRetentionResult{}, false, fmt.Errorf("%w: subject identity: %v", ErrReconciliationRetentionQuery, err)
	}

	var row struct {
		ID      string `bun:"reconciliation_id"`
		Version int64  `bun:"reconciliation_version"`
	}
	var err error
	if query.TenantID == "" {
		err = s.db.NewRaw(latestRetentionSubjectSQL, s.storeID, ReconciliationRecordSchemaRetention, string(query.SubjectKind), query.SubjectID).Scan(ctx, &row)
	} else {
		err = s.db.NewRaw(latestRetentionSubjectTenantSQL, s.storeID, ReconciliationRecordSchemaRetention, string(query.SubjectKind), query.SubjectID, query.TenantID).Scan(ctx, &row)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return billing.ReconciliationRetentionResult{}, false, nil
	}
	if err != nil {
		return billing.ReconciliationRetentionResult{}, false, fmt.Errorf("billingstore: latest reconciliation retention: %w", err)
	}
	result, err := s.GetReconciliationRetention(ctx, row.ID, uint64(row.Version))
	if err != nil {
		return billing.ReconciliationRetentionResult{}, false, err
	}
	return result, true, nil
}

func reconciliationRetentionFromRecord(record ReconciliationRecord) (billing.ReconciliationRetentionResult, error) {
	if record.ResultSchemaVersion != ReconciliationRecordSchemaRetention {
		return billing.ReconciliationRetentionResult{}, fmt.Errorf("%w: id=%q version=%d schema=%d", ErrReconciliationRetentionMismatch, record.ID, record.Version, record.ResultSchemaVersion)
	}
	result, err := billing.ParseReconciliationRetentionResult(record.ResultJSON)
	if err != nil {
		return billing.ReconciliationRetentionResult{}, fmt.Errorf("%w: id=%q version=%d: %v", ErrReconciliationRetentionMismatch, record.ID, record.Version, err)
	}
	if err := bindReconciliationRetentionEnvelope(record, result); err != nil {
		return billing.ReconciliationRetentionResult{}, fmt.Errorf("%w: id=%q version=%d: %w", ErrReconciliationRetentionMismatch, record.ID, record.Version, err)
	}
	return result, nil
}

// bindReconciliationRetentionEnvelope rejects a schema-2 outer reconciliation
// envelope that is not the authoritative ReconciliationRecordFromRetention
// mapping of the parsed inner full result. Every persisted identity field is
// compared exactly, and caller-owned outer values are never rewritten to make
// them match.
func bindReconciliationRetentionEnvelope(record ReconciliationRecord, result billing.ReconciliationRetentionResult) error {
	if record.ResultSchemaVersion != ReconciliationRecordSchemaRetention {
		return fmt.Errorf("%w: schema version %d is not a retention envelope", ErrReconciliationRetentionMismatch, record.ResultSchemaVersion)
	}
	for name, mismatch := range map[string]bool{
		"reconciliation id":   record.ID != result.ID,
		"revision":            record.Version != result.ResultRevision,
		"subject":             !reconciliationEnvelopeSubjectEqual(record.Subject, result.Subject),
		"scope":               record.Scope != result.Scope,
		"input_set_hash":      record.InputSetHash != result.InputSetHash,
		"local_input_hash":    record.LocalInputHash != result.LocalInputHash,
		"provider_input_hash": record.ProviderInputHash != result.ProviderInputHash,
		"policy_id":           record.PolicyID != result.Policy.ID,
		"policy_version":      record.PolicyVersion != result.Policy.Version,
		"created_at":          !record.CreatedAt.Equal(result.CreatedAt),
	} {
		if mismatch {
			return fmt.Errorf("%w: %s does not match the canonical inner result", ErrReconciliationRetentionMismatch, name)
		}
	}
	return nil
}

// reconciliationEnvelopeSubjectEqual compares the persisted subject identity
// through its canonical JSON encoding, so two subjects that serialize
// identically are one identity and any field mismatch is rejected.
func reconciliationEnvelopeSubjectEqual(left, right metering.SubjectRef) bool {
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return false
	}
	return string(leftJSON) == string(rightJSON)
}

// reconciliationStoredProjection is the schema-2 row projection that must
// agree with the decoded canonical envelope.
type reconciliationStoredProjection struct {
	SubjectKind         string
	SubjectID           string
	SubjectJSON         string
	TenantID            string
	Scope               string
	Basis               string
	ResultSchemaVersion int64
	InputSetHash        string
	LocalInputHash      string
	ProviderInputHash   string
	PolicyID            string
	PolicyVersion       string
	CreatedAt           int64
	ProjectionVersion   int64
}

// bindReconciliationStoredProjection rejects a canonical schema-2 envelope
// whose stored projection columns disagree with it, so subject/tenant
// projections and latest lookups can never return a row the envelope does not
// own.
func bindReconciliationStoredProjection(record ReconciliationRecord, projection reconciliationStoredProjection) error {
	if projection.ResultSchemaVersion != int64(ReconciliationRecordSchemaRetention) {
		return fmt.Errorf("%w: schema version %d is not a retention projection", ErrReconciliationRetentionMismatch, projection.ResultSchemaVersion)
	}
	subjectJSON, err := json.Marshal(record.Subject)
	if err != nil {
		return fmt.Errorf("%w: subject encode: %v", ErrReconciliationRetentionMismatch, err)
	}
	for name, mismatch := range map[string]bool{
		"subject_kind":        projection.SubjectKind != string(record.Subject.Kind),
		"subject_id":          projection.SubjectID != subjectIDForEconomics(record.Subject),
		"subject_json":        projection.SubjectJSON != string(subjectJSON),
		"tenant_id":           projection.TenantID != record.Subject.TenantID,
		"scope":               projection.Scope != record.Scope,
		"basis":               projection.Basis != string(record.Basis),
		"input_set_hash":      projection.InputSetHash != record.InputSetHash,
		"local_input_hash":    projection.LocalInputHash != record.LocalInputHash,
		"provider_input_hash": projection.ProviderInputHash != record.ProviderInputHash,
		"policy_id":           projection.PolicyID != record.PolicyID,
		"policy_version":      projection.PolicyVersion != record.PolicyVersion,
		"created_at":          !time.Unix(0, projection.CreatedAt).UTC().Equal(record.CreatedAt),
		"projection_version":  projection.ProjectionVersion != BillingEconomicsProjectionVersion,
	} {
		if mismatch {
			return fmt.Errorf("%w: stored %s projection does not match the canonical envelope", ErrReconciliationRetentionMismatch, name)
		}
	}
	return nil
}
