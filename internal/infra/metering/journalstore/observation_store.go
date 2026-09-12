package journalstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
)

const (
	componentItemMeasure        = "measure"
	componentItemReportedCharge = "reported_charge"
	v2CursorPrefix              = "v2."
	v2CursorVersion             = 1
	v2HardPageMaximum           = 500
)

// ObservationTxWriter is the infra-only seam used by a composition root to
// append an observation on an already-open local transaction. Bun remains
// inside infrastructure and is never part of the SDK/domain contracts.
type ObservationTxWriter interface {
	AppendObservationInTx(context.Context, bun.Tx, metering.Observation) error
}

// ObservationQuery is a store-scoped, bounded V2 observation query. At least
// one selective bound (subject identity, stream, tenant, or component) is
// required. StoreID is optional and, when set, must equal the opened store.
type ObservationQuery struct {
	StoreID               string
	SubjectKind           metering.SubjectKind
	SubjectID             string
	Subject               *metering.SubjectRef
	TenantID              string
	ProviderAccountKey    string
	StreamID              string
	ComponentKey          string
	ComponentCanonicalKey string
	ComponentKeyHash      string
	ItemKind              string
	Limit                 int
	Cursor                string
}

// ObservationPage is one deterministic page of immutable V2 observations.
type ObservationPage struct {
	Observations []metering.Observation
	NextCursor   string
}

// ComponentProjection is a rebuildable, indexed projection of one V2 measure
// or reported charge. ComponentKey is the canonical key bytes; the hash is
// only an accelerator and is never authoritative.
type ComponentProjection struct {
	ID                     int64
	StoreID                string
	ObservationRowID       int64
	ObservationID          string
	ObservationRevision    uint64
	ObservationFingerprint string
	ItemKind               string
	ItemID                 string
	ComponentKey           string
	CanonicalComponentKey  string
	ComponentKeyHash       string
	Coefficient            string
	Scale                  uint8
	ValuePresent           bool
	MoneyPresent           bool
	Currency               string
	ChargeCoverageJSON     string
	SubjectKind            metering.SubjectKind
	SubjectID              string
	TenantID               string
	ProviderAccountKey     string
	StreamID               string
	Sequence               uint64
	Origin                 string
	Acquisition            string
	Authority              string
	ProjectionVersion      int
}

// ComponentQuery is a store-scoped, bounded component projection query.
type ComponentQuery struct {
	StoreID               string
	SubjectKind           metering.SubjectKind
	SubjectID             string
	Subject               *metering.SubjectRef
	TenantID              string
	ProviderAccountKey    string
	StreamID              string
	ComponentKey          string
	ComponentCanonicalKey string
	ComponentKeyHash      string
	ItemKind              string
	Limit                 int
	Cursor                string
}

// ComponentPage is one deterministic page of projected observation items.
type ComponentPage struct {
	Components []ComponentProjection
	NextCursor string
}

type observationCursor struct {
	Version       int    `json:"version"`
	Kind          string `json:"kind"`
	ItemKind      string `json:"item_kind,omitempty"`
	StoreID       string `json:"store_id"`
	FilterHash    string `json:"filter_hash"`
	StreamID      string `json:"stream_id"`
	Sequence      int64  `json:"sequence"`
	ObservationID string `json:"observation_id"`
	Revision      int64  `json:"revision"`
	ItemID        string `json:"item_id,omitempty"`
	RowID         int64  `json:"row_id"`
}

func (s *DurableStore) DB() *bun.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Database is an explicit infra spelling of DB for composition checks.
func (s *DurableStore) Database() *bun.DB { return s.DB() }

// SetObservationFaultHook installs a test/failpoint hook. A returned error
// aborts the active append transaction.
func (s *DurableStore) SetObservationFaultHook(hook func(string) error) {
	if s != nil {
		s.cfg.ObservationFaultHook = hook
	}
}

func (s *DurableStore) observationFault(stage string) error {
	if s == nil || s.cfg.ObservationFaultHook == nil {
		return nil
	}
	if err := s.cfg.ObservationFaultHook(stage); err != nil {
		return fmt.Errorf("metering/journalstore: observation failpoint %s: %w", stage, err)
	}
	return nil
}

func canonicalObservationForStore(storeID string, observation metering.Observation) (metering.Observation, []byte, error) {
	if strings.TrimSpace(storeID) == "" {
		return metering.Observation{}, nil, fmt.Errorf("metering/journalstore: store id is required")
	}
	if observation.Subject.StoreID != storeID || observation.Correlation.StoreID != storeID {
		return metering.Observation{}, nil, fmt.Errorf("%w: observation store mismatch", ErrQueryOutOfScope)
	}
	canonical, err := observation.Canonical()
	if err != nil {
		return metering.Observation{}, nil, fmt.Errorf("metering/journalstore: observation: %w", err)
	}
	payload, err := canonical.CanonicalJSON()
	if err != nil {
		return metering.Observation{}, nil, fmt.Errorf("metering/journalstore: observation canonical JSON: %w", err)
	}
	return canonical, payload, nil
}

// AppendObservation appends one immutable V2 observation and all of its
// measure/charge projections atomically. Exact replay is a no-op; a changed
// payload for the same source identity/revision is a collision.
func (s *DurableStore) AppendObservation(ctx context.Context, observation metering.Observation) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("metering/journalstore: nil store")
	}
	if ctx == nil {
		return fmt.Errorf("metering/journalstore: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("metering/journalstore: observation begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.AppendObservationInTx(ctx, tx, observation); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("metering/journalstore: observation commit: %w", err)
	}
	return nil
}

// AppendObservationInTx appends an observation using the caller-owned local
// transaction. It never commits or rolls back tx.
func (s *DurableStore) AppendObservationInTx(ctx context.Context, tx bun.Tx, observation metering.Observation) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("metering/journalstore: nil store")
	}
	if ctx == nil {
		return fmt.Errorf("metering/journalstore: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	canonical, payload, err := canonicalObservationForStore(s.cfg.StoreID, observation)
	if err != nil {
		return err
	}
	identity := canonical.SourceEventIdentity()
	fingerprint := canonical.Fingerprint()
	if fingerprint == "" {
		return fmt.Errorf("metering/journalstore: observation fingerprint unavailable")
	}
	if canonical.Revision > math.MaxInt64 || canonical.Sequence > math.MaxInt64 {
		return fmt.Errorf("metering/journalstore: observation identity exceeds database integer range")
	}

	if existing, found, lookupErr := lookupObservationRow(ctx, tx, s.cfg.StoreID, identity, canonical.ID, int64(canonical.Revision)); lookupErr != nil {
		return lookupErr
	} else if found {
		return resolveObservationReplay(existing, payload, canonical)
	}
	if err := s.insertObservationRow(ctx, tx, canonical, payload, identity, fingerprint); err != nil {
		if !isUniqueViolation(err) {
			return err
		}
		// A concurrent writer may have won either source identity, observation
		// identity, or the inherited V1 stream/fact unique index. The same tx
		// is unusable on PostgreSQL after a conflict, so callers should retry;
		// ordinary replay is resolved before insert and does not take this path.
		return fmt.Errorf("metering/journalstore: observation unique race: %w", err)
	}
	var row observationRow
	lookupErr := tx.NewRaw(`
SELECT id, payload_json, payload_kind, observation_fingerprint
FROM metering_facts
WHERE store_id = ? AND payload_kind = 'observation' AND source_event_key = ?
	LIMIT 1`, s.cfg.StoreID, identity).Scan(ctx, &row)
	if errors.Is(lookupErr, sql.ErrNoRows) {
		// A different source identity can still collide with the durable
		// observation ID/revision uniqueness fence. Resolve that case as the
		// same typed replay/collision outcome rather than leaking a misleading
		// post-insert lookup failure.
		lookupErr = tx.NewRaw(`
SELECT id, payload_json, payload_kind, observation_fingerprint
FROM metering_facts
WHERE store_id = ? AND payload_kind = 'observation' AND observation_id = ? AND observation_revision = ?
LIMIT 1`, s.cfg.StoreID, canonical.ID, int64(canonical.Revision)).Scan(ctx, &row)
	}
	if lookupErr != nil {
		return fmt.Errorf("metering/journalstore: observation row lookup after insert: %w", lookupErr)
	}
	if row.PayloadKind != "observation" {
		return fmt.Errorf("%w: source identity is occupied by a V1 fact", ErrIdentityCollision)
	}
	if row.Payload != string(payload) || (row.ObservationFingerprint != "" && row.ObservationFingerprint != fingerprint) {
		return fmt.Errorf("%w: observation_id=%q revision=%d", ErrIdentityCollision, canonical.ID, canonical.Revision)
	}
	if err := s.observationFault("after_canonical"); err != nil {
		return err
	}
	if err := insertObservationComponents(ctx, tx, s.cfg.StoreID, row.ID, canonical, fingerprint); err != nil {
		return err
	}
	if err := s.observationFault("after_projections"); err != nil {
		return err
	}
	return nil
}

// WriteObservationTx is a compatibility spelling for infra composition code.
func (s *DurableStore) WriteObservationTx(ctx context.Context, tx bun.Tx, observation metering.Observation) error {
	return s.AppendObservationInTx(ctx, tx, observation)
}

type observationRow struct {
	ID                     int64  `bun:"id"`
	Payload                string `bun:"payload_json"`
	PayloadKind            string `bun:"payload_kind"`
	ObservationFingerprint string `bun:"observation_fingerprint"`
}

func lookupObservationRow(ctx context.Context, q bun.IDB, storeID, identity, observationID string, revision int64) (observationRow, bool, error) {
	var row observationRow
	err := q.NewRaw(`
SELECT id, payload_json, payload_kind, observation_fingerprint
FROM metering_facts
WHERE store_id = ? AND source_event_key = ?
LIMIT 1`, storeID, identity).Scan(ctx, &row)
	if err == nil {
		return row, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return observationRow{}, false, fmt.Errorf("metering/journalstore: observation identity lookup: %w", err)
	}
	err = q.NewRaw(`
SELECT id, payload_json, payload_kind, observation_fingerprint
FROM metering_facts
WHERE store_id = ? AND payload_kind = 'observation' AND observation_id = ? AND observation_revision = ?
LIMIT 1`, storeID, observationID, revision).Scan(ctx, &row)
	if err == nil {
		return row, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return observationRow{}, false, fmt.Errorf("metering/journalstore: observation revision lookup: %w", err)
	}
	return observationRow{}, false, nil
}

func resolveObservationReplay(existing observationRow, payload []byte, observation metering.Observation) error {
	if existing.PayloadKind != "observation" {
		return fmt.Errorf("%w: source identity is occupied by a V1 fact", ErrIdentityCollision)
	}
	if existing.Payload == string(payload) && (existing.ObservationFingerprint == "" || existing.ObservationFingerprint == observation.Fingerprint()) {
		return nil
	}
	return fmt.Errorf("%w: observation_id=%q revision=%d", ErrIdentityCollision, observation.ID, observation.Revision)
}

func (s *DurableStore) insertObservationRow(ctx context.Context, tx bun.Tx, observation metering.Observation, payload []byte, identity, fingerprint string) error {
	// The legacy V2 schema keeps a unique (store_id, stream_id, fact_id)
	// constraint. Observation ID is the replay identity, but a new observation
	// revision must still append a distinct canonical row, so keep the public
	// observation_id in its additive column and use a revision-qualified
	// internal fact ID for the inherited column.
	factID := fmt.Sprintf("observation:%s:%d", observation.ID, observation.Revision)
	if _, err := tx.NewRaw(`
INSERT INTO metering_facts(
	store_id, fact_id, stream_id, sequence, source_event_key, fact_kind,
	perspective, boundary, lifecycle_scope,
	request_id, a_leg_id, b_leg_id, attempt_id,
	frontend_id, backend_id, model, presence, source, authority,
	recorded_at_unix, payload_json,
	identity_version, source_revision, source_event_kind, source_id,
	payload_kind, observation_id, observation_revision, observation_fingerprint,
	observation_subject_kind, observation_subject_id, observation_tenant_id,
	observation_origin, observation_acquisition, observation_provider_account_key
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT DO NOTHING
	`, s.cfg.StoreID,
		factID,
		observation.StreamID,
		int64(observation.Sequence),
		identity,
		observation.Semantics,
		string(observation.Perspective),
		string(observation.Boundary),
		string(observation.Lifecycle),
		observation.Correlation.RequestID,
		observation.Correlation.ALegID,
		observation.Correlation.BLegID,
		observation.Correlation.AttemptID,
		"", "", "", "", observation.Acquisition, observation.Authority,
		observation.ReceivedAt.UnixNano(),
		string(payload),
		int64(observation.Version),
		int64(observation.Revision),
		observation.Semantics,
		observation.ID,
		"observation",
		observation.ID,
		int64(observation.Revision),
		fingerprint,
		string(observation.Subject.Kind),
		subjectID(observation.Subject),
		observationTenant(observation),
		observation.Origin,
		observation.Acquisition,
		observationProviderAccount(observation),
	).Exec(ctx); err != nil {
		return fmt.Errorf("metering/journalstore: insert observation: %w", err)
	}
	return nil
}

func subjectID(subject metering.SubjectRef) string {
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

func insertObservationComponents(ctx context.Context, q bun.IDB, storeID string, rowID int64, observation metering.Observation, fingerprint string) error {
	for _, measure := range observation.Measures {
		key := measure.Key.CanonicalKey()
		if key == "" {
			return fmt.Errorf("metering/journalstore: measure canonical component key is empty")
		}
		coefficient, scale := decimalParts(measure.Value)
		if err := insertComponent(ctx, q, componentProjectionArgs{
			StoreID: storeID, ObservationRowID: rowID, ObservationID: observation.ID, ObservationRevision: observation.Revision,
			ObservationFingerprint: fingerprint, ItemKind: componentItemMeasure, ItemID: key,
			ComponentKey: key, ComponentKeyHash: measure.Key.Fingerprint(), Coefficient: coefficient, Scale: scale,
			ValuePresent: measure.Value != nil, MoneyPresent: false, Subject: observation.Subject, TenantID: observationTenant(observation), StreamID: observation.StreamID,
			ProviderAccountKey: observationProviderAccount(observation),
			Sequence:           observation.Sequence, Origin: observation.Origin, Acquisition: observation.Acquisition, Authority: observation.Authority,
		}); err != nil {
			return err
		}
	}
	for _, charge := range observation.Charges {
		componentKey, componentHash := "", ""
		if charge.Component != nil {
			componentKey = charge.Component.CanonicalKey()
			componentHash = charge.Component.Fingerprint()
			if componentKey == "" || componentHash == "" {
				return fmt.Errorf("metering/journalstore: charge canonical component key is empty")
			}
		}
		coverage := []metering.ChargeCoverageRef{}
		if charge.Covers != nil {
			coverage = append(coverage, charge.Covers...)
		}
		coverageJSON, err := json.Marshal(coverage)
		if err != nil {
			return fmt.Errorf("metering/journalstore: charge coverage JSON: %w", err)
		}
		coefficient, scale := decimalParts(charge.Amount)
		if err := insertComponent(ctx, q, componentProjectionArgs{
			StoreID: storeID, ObservationRowID: rowID, ObservationID: observation.ID, ObservationRevision: observation.Revision,
			ObservationFingerprint: fingerprint, ItemKind: componentItemReportedCharge, ItemID: charge.ChargeItemID,
			ComponentKey: componentKey, ComponentKeyHash: componentHash, Coefficient: coefficient, Scale: scale,
			ValuePresent: false, MoneyPresent: charge.Amount != nil, Currency: charge.Currency, TenantID: observationTenant(observation),
			ChargeCoverageJSON: string(coverageJSON), Subject: observation.Subject, StreamID: observation.StreamID,
			ProviderAccountKey: observationProviderAccount(observation),
			Sequence:           observation.Sequence, Origin: observation.Origin, Acquisition: observation.Acquisition, Authority: observation.Authority,
		}); err != nil {
			return err
		}
	}
	return nil
}

func observationProviderAccount(observation metering.Observation) string {
	if observation.Correlation.ProviderAccountKey != "" {
		return observation.Correlation.ProviderAccountKey
	}
	return observation.Subject.ProviderAccountKey
}

func observationTenant(observation metering.Observation) string {
	if observation.Subject.TenantID != "" {
		return observation.Subject.TenantID
	}
	return observation.Correlation.TenantID
}

func decimalParts(value *metering.Decimal) (string, int) {
	if value == nil {
		return "", 0
	}
	normalized, err := value.Normalize()
	if err != nil {
		return value.Coefficient, int(value.Scale)
	}
	return normalized.Coefficient, int(normalized.Scale)
}

type componentProjectionArgs struct {
	StoreID                string
	ObservationRowID       int64
	ObservationID          string
	ObservationRevision    uint64
	ObservationFingerprint string
	ItemKind               string
	ItemID                 string
	ComponentKey           string
	ComponentKeyHash       string
	Coefficient            string
	Scale                  int
	ValuePresent           bool
	MoneyPresent           bool
	Currency               string
	ChargeCoverageJSON     string
	Subject                metering.SubjectRef
	TenantID               string
	ProviderAccountKey     string
	StreamID               string
	Sequence               uint64
	Origin                 string
	Acquisition            string
	Authority              string
}

func insertComponent(ctx context.Context, q bun.IDB, args componentProjectionArgs) error {
	if args.ChargeCoverageJSON == "" {
		args.ChargeCoverageJSON = "[]"
	}
	if args.ObservationRevision > math.MaxInt64 || args.Sequence > math.MaxInt64 {
		return fmt.Errorf("metering/journalstore: component integer identity overflow")
	}
	_, err := q.NewRaw(`
INSERT INTO metering_components(
	store_id, observation_row_id, observation_id, observation_revision, observation_fingerprint,
	item_kind, item_id, component_key, component_key_hash, coefficient, scale,
	value_present, money_present, currency, charge_coverage_json,
	subject_kind, subject_id, tenant_id, provider_account_key, stream_id, sequence, origin, acquisition, authority, projection_version
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`, args.StoreID, args.ObservationRowID, args.ObservationID, int64(args.ObservationRevision), args.ObservationFingerprint,
		args.ItemKind, args.ItemID, args.ComponentKey, args.ComponentKeyHash, args.Coefficient, args.Scale,
		args.ValuePresent, args.MoneyPresent, args.Currency, args.ChargeCoverageJSON,
		string(args.Subject.Kind), subjectID(args.Subject), args.TenantID, args.ProviderAccountKey, args.StreamID, int64(args.Sequence), args.Origin, args.Acquisition, args.Authority, ObservationProjectionVersion,
	).Exec(ctx)
	if err != nil {
		return fmt.Errorf("metering/journalstore: insert component projection: %w", err)
	}
	return nil
}

func normalizeV2StoreID(storeID, opened string) (string, error) {
	storeID = strings.TrimSpace(storeID)
	if storeID == "" {
		storeID = opened
	}
	if storeID != opened {
		return "", fmt.Errorf("%w: store_id=%q", ErrQueryOutOfScope, storeID)
	}
	return storeID, nil
}

func normalizeV2Limit(limit, defaultLimit int) (int, error) {
	if limit < 0 {
		return 0, fmt.Errorf("%w: negative limit", ErrPageSizeExceeded)
	}
	if limit > v2HardPageMaximum {
		return 0, ErrPageSizeExceeded
	}
	if limit == 0 {
		limit = defaultLimit
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > v2HardPageMaximum {
		limit = v2HardPageMaximum
	}
	return limit, nil
}

func subjectQueryValues(kind metering.SubjectKind, id string, subject *metering.SubjectRef) (metering.SubjectKind, string, error) {
	if subject != nil {
		if err := subject.Validate(); err != nil {
			return "", "", fmt.Errorf("%w: subject: %v", ErrQueryOutOfScope, err)
		}
		if kind != "" && kind != subject.Kind {
			return "", "", fmt.Errorf("%w: subject kind mismatch", ErrQueryOutOfScope)
		}
		if id != "" && id != subjectID(*subject) {
			return "", "", fmt.Errorf("%w: subject id mismatch", ErrQueryOutOfScope)
		}
		kind, id = subject.Kind, subjectID(*subject)
	}
	if kind != "" && !kind.IsKnown() {
		return "", "", fmt.Errorf("%w: unknown subject kind", ErrQueryOutOfScope)
	}
	if (kind == "") != (id == "") {
		return "", "", fmt.Errorf("%w: subject kind and id must be supplied together", ErrQueryOutOfScope)
	}
	return kind, id, nil
}

func validComponentFilter(key, hash string) error {
	if key != "" && !json.Valid([]byte(key)) {
		return fmt.Errorf("%w: component key is not canonical JSON", ErrQueryOutOfScope)
	}
	if hash != "" {
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != sha256.Size {
			return fmt.Errorf("%w: component key hash must be SHA-256 hex", ErrQueryOutOfScope)
		}
	}
	return nil
}

func filterHash(value any) string {
	b, _ := json.Marshal(value)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func encodeObservationCursor(cursor observationCursor) string {
	b, _ := json.Marshal(cursor)
	return v2CursorPrefix + base64.RawURLEncoding.EncodeToString(b)
}

func decodeObservationCursor(raw, kind, storeID, hash string) (observationCursor, error) {
	if raw == "" {
		return observationCursor{}, nil
	}
	if !strings.HasPrefix(raw, v2CursorPrefix) {
		return observationCursor{}, ErrInvalidCursor
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, v2CursorPrefix))
	if err != nil {
		return observationCursor{}, fmt.Errorf("%w: base64", ErrInvalidCursor)
	}
	var cursor observationCursor
	if err := json.Unmarshal(b, &cursor); err != nil {
		return observationCursor{}, fmt.Errorf("%w: JSON", ErrInvalidCursor)
	}
	if cursor.Version != v2CursorVersion || cursor.Kind != kind || cursor.StoreID != storeID || cursor.FilterHash != hash || cursor.RowID <= 0 || cursor.Revision <= 0 || cursor.Sequence < 0 || cursor.StreamID == "" || cursor.ObservationID == "" {
		return observationCursor{}, ErrInvalidCursor
	}
	if kind == "components" {
		if cursor.ItemKind != componentItemMeasure && cursor.ItemKind != componentItemReportedCharge || cursor.ItemID == "" {
			return observationCursor{}, ErrInvalidCursor
		}
	} else if cursor.ItemKind != "" || cursor.ItemID != "" {
		return observationCursor{}, ErrInvalidCursor
	}
	return cursor, nil
}

// GetObservation returns one exact V2 observation revision.
func (s *DurableStore) GetObservation(ctx context.Context, observationID string, revision uint64) (metering.Observation, error) {
	if s == nil || s.db == nil {
		return metering.Observation{}, fmt.Errorf("metering/journalstore: nil store")
	}
	if ctx == nil {
		return metering.Observation{}, fmt.Errorf("metering/journalstore: nil context")
	}
	if err := ctx.Err(); err != nil {
		return metering.Observation{}, err
	}
	if revision > math.MaxInt64 {
		return metering.Observation{}, fmt.Errorf("metering/journalstore: observation revision exceeds database range")
	}
	var payload string
	err := s.db.NewRaw(`SELECT payload_json FROM metering_facts WHERE store_id = ? AND payload_kind = 'observation' AND observation_id = ? AND observation_revision = ? LIMIT 1`, s.cfg.StoreID, observationID, int64(revision)).Scan(ctx, &payload)
	if err != nil {
		return metering.Observation{}, fmt.Errorf("metering/journalstore: get observation: %w", err)
	}
	var observation metering.Observation
	if err := json.Unmarshal([]byte(payload), &observation); err != nil {
		return metering.Observation{}, fmt.Errorf("metering/journalstore: decode observation: %w", err)
	}
	return observation, nil
}

// GetObservationRef is a convenience form for callers holding an immutable
// observation reference.
func (s *DurableStore) GetObservationRef(ctx context.Context, ref metering.ObservationRef) (metering.Observation, error) {
	if s == nil || s.db == nil {
		return metering.Observation{}, fmt.Errorf("metering/journalstore: nil store")
	}
	if ctx == nil {
		return metering.Observation{}, fmt.Errorf("metering/journalstore: nil context")
	}
	if ref.StoreID != s.cfg.StoreID {
		return metering.Observation{}, fmt.Errorf("%w: observation ref store", ErrQueryOutOfScope)
	}
	observation, err := s.GetObservation(ctx, ref.ObservationID, ref.Revision)
	if err != nil {
		return metering.Observation{}, err
	}
	if ref.PayloadHash != "" && observation.Fingerprint() != ref.PayloadHash {
		return metering.Observation{}, fmt.Errorf("%w: observation payload hash mismatch", ErrIdentityCollision)
	}
	return observation, nil
}

func (s *DurableStore) ListObservations(ctx context.Context, query ObservationQuery) (ObservationPage, error) {
	if s == nil || s.db == nil {
		return ObservationPage{}, fmt.Errorf("metering/journalstore: nil store")
	}
	if ctx == nil {
		return ObservationPage{}, fmt.Errorf("metering/journalstore: nil context")
	}
	storeID, err := normalizeV2StoreID(query.StoreID, s.cfg.StoreID)
	if err != nil {
		return ObservationPage{}, err
	}
	kind, subject, err := subjectQueryValues(query.SubjectKind, strings.TrimSpace(query.SubjectID), query.Subject)
	if err != nil {
		return ObservationPage{}, err
	}
	if query.Subject != nil && query.Subject.StoreID != storeID {
		return ObservationPage{}, fmt.Errorf("%w: subject store", ErrQueryOutOfScope)
	}
	tenantID := strings.TrimSpace(query.TenantID)
	if query.Subject != nil && query.Subject.TenantID != "" {
		if tenantID != "" && tenantID != query.Subject.TenantID {
			return ObservationPage{}, fmt.Errorf("%w: subject tenant", ErrQueryOutOfScope)
		}
		tenantID = query.Subject.TenantID
	}
	componentKey := query.ComponentCanonicalKey
	if componentKey == "" {
		componentKey = query.ComponentKey
	}
	if err := validComponentFilter(componentKey, query.ComponentKeyHash); err != nil {
		return ObservationPage{}, err
	}
	if query.ItemKind != "" && query.ItemKind != componentItemMeasure && query.ItemKind != componentItemReportedCharge {
		return ObservationPage{}, fmt.Errorf("%w: unknown component item kind", ErrQueryOutOfScope)
	}
	if kind == "" && tenantID == "" && query.ProviderAccountKey == "" && query.StreamID == "" && componentKey == "" && query.ComponentKeyHash == "" {
		return ObservationPage{}, ErrQueryTooBroad
	}
	limit, err := normalizeV2Limit(query.Limit, s.defaultPageSize)
	if err != nil {
		return ObservationPage{}, err
	}
	filter := struct {
		StoreID, SubjectKind, SubjectID, TenantID, ProviderAccountKey, StreamID, ComponentKey, ComponentKeyHash, ItemKind string
	}{storeID, string(kind), subject, tenantID, query.ProviderAccountKey, query.StreamID, componentKey, query.ComponentKeyHash, query.ItemKind}
	hash := filterHash(filter)
	position, err := decodeObservationCursor(query.Cursor, "observations", storeID, hash)
	if err != nil {
		return ObservationPage{}, err
	}
	where := []string{"f.store_id = ?", "f.payload_kind = 'observation'"}
	args := []any{storeID}
	if kind != "" {
		where = append(where, "f.observation_subject_kind = ?", "f.observation_subject_id = ?")
		args = append(args, string(kind), subject)
	}
	if tenantID != "" {
		where = append(where, "f.observation_tenant_id = ?")
		args = append(args, tenantID)
	}
	if query.ProviderAccountKey != "" {
		where = append(where, "f.observation_provider_account_key = ?")
		args = append(args, query.ProviderAccountKey)
	}
	if query.StreamID != "" {
		where = append(where, "f.stream_id = ?")
		args = append(args, query.StreamID)
	}
	if componentKey != "" || query.ComponentKeyHash != "" || query.ItemKind != "" {
		exists := []string{"mc.observation_row_id = f.id", "mc.store_id = f.store_id"}
		if componentKey != "" {
			exists = append(exists, "mc.component_key = ?")
			args = append(args, componentKey)
		}
		if query.ComponentKeyHash != "" {
			exists = append(exists, "mc.component_key_hash = ?")
			args = append(args, query.ComponentKeyHash)
		}
		if query.ItemKind != "" {
			exists = append(exists, "mc.item_kind = ?")
			args = append(args, query.ItemKind)
		}
		where = append(where, "EXISTS (SELECT 1 FROM metering_components mc WHERE "+strings.Join(exists, " AND ")+")")
	}
	if query.Cursor != "" {
		where = append(where, `(f.stream_id > ? OR (f.stream_id = ? AND (f.sequence > ? OR (f.sequence = ? AND (f.observation_id > ? OR (f.observation_id = ? AND (f.observation_revision > ? OR (f.observation_revision = ? AND f.id > ?))))))))`)
		args = append(args, position.StreamID, position.StreamID, position.Sequence, position.Sequence, position.ObservationID, position.ObservationID, position.Revision, position.Revision, position.RowID)
	}
	querySQL := `SELECT f.payload_json, f.stream_id, f.sequence, f.observation_id, f.observation_revision, f.id FROM metering_facts f WHERE ` + strings.Join(where, " AND ") + ` ORDER BY f.stream_id ASC, f.sequence ASC, f.observation_id ASC, f.observation_revision ASC, f.id ASC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return ObservationPage{}, fmt.Errorf("metering/journalstore: list observations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type row struct {
		Payload       string
		StreamID      string
		Sequence      int64
		ObservationID string
		Revision      int64
		ID            int64
	}
	items := make([]row, 0, limit)
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.Payload, &item.StreamID, &item.Sequence, &item.ObservationID, &item.Revision, &item.ID); err != nil {
			return ObservationPage{}, fmt.Errorf("metering/journalstore: scan observations: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ObservationPage{}, err
	}
	page := ObservationPage{Observations: make([]metering.Observation, 0, minInt(len(items), limit))}
	if len(items) > limit {
		last := items[limit-1]
		page.NextCursor = encodeObservationCursor(observationCursor{Version: v2CursorVersion, Kind: "observations", StoreID: storeID, FilterHash: hash, StreamID: last.StreamID, Sequence: last.Sequence, ObservationID: last.ObservationID, Revision: last.Revision, RowID: last.ID})
		items = items[:limit]
	}
	for _, item := range items {
		var observation metering.Observation
		if err := json.Unmarshal([]byte(item.Payload), &observation); err != nil {
			return ObservationPage{}, fmt.Errorf("metering/journalstore: decode listed observation: %w", err)
		}
		page.Observations = append(page.Observations, observation)
	}
	return page, nil
}

// ListObservationComponents returns deterministic bounded projection rows.
func (s *DurableStore) ListObservationComponents(ctx context.Context, query ComponentQuery) (ComponentPage, error) {
	if s == nil || s.db == nil {
		return ComponentPage{}, fmt.Errorf("metering/journalstore: nil store")
	}
	if ctx == nil {
		return ComponentPage{}, fmt.Errorf("metering/journalstore: nil context")
	}
	storeID, err := normalizeV2StoreID(query.StoreID, s.cfg.StoreID)
	if err != nil {
		return ComponentPage{}, err
	}
	kind, subject, err := subjectQueryValues(query.SubjectKind, strings.TrimSpace(query.SubjectID), query.Subject)
	if err != nil {
		return ComponentPage{}, err
	}
	if query.Subject != nil && query.Subject.StoreID != storeID {
		return ComponentPage{}, fmt.Errorf("%w: subject store", ErrQueryOutOfScope)
	}
	tenantID := strings.TrimSpace(query.TenantID)
	if query.Subject != nil && query.Subject.TenantID != "" {
		if tenantID != "" && tenantID != query.Subject.TenantID {
			return ComponentPage{}, fmt.Errorf("%w: subject tenant", ErrQueryOutOfScope)
		}
		tenantID = query.Subject.TenantID
	}
	componentKey := query.ComponentCanonicalKey
	if componentKey == "" {
		componentKey = query.ComponentKey
	}
	if err := validComponentFilter(componentKey, query.ComponentKeyHash); err != nil {
		return ComponentPage{}, err
	}
	if query.ItemKind != "" && query.ItemKind != componentItemMeasure && query.ItemKind != componentItemReportedCharge {
		return ComponentPage{}, fmt.Errorf("%w: unknown component item kind", ErrQueryOutOfScope)
	}
	if kind == "" && tenantID == "" && query.ProviderAccountKey == "" && query.StreamID == "" && componentKey == "" && query.ComponentKeyHash == "" && query.ItemKind == "" {
		return ComponentPage{}, ErrQueryTooBroad
	}
	limit, err := normalizeV2Limit(query.Limit, s.defaultPageSize)
	if err != nil {
		return ComponentPage{}, err
	}
	filter := struct {
		StoreID, SubjectKind, SubjectID, TenantID, ProviderAccountKey, StreamID, ComponentKey, ComponentKeyHash, ItemKind string
	}{storeID, string(kind), subject, tenantID, query.ProviderAccountKey, query.StreamID, componentKey, query.ComponentKeyHash, query.ItemKind}
	hash := filterHash(filter)
	position, err := decodeObservationCursor(query.Cursor, "components", storeID, hash)
	if err != nil {
		return ComponentPage{}, err
	}
	where := []string{"mc.store_id = ?"}
	args := []any{storeID}
	if kind != "" {
		where = append(where, "mc.subject_kind = ?", "mc.subject_id = ?")
		args = append(args, string(kind), subject)
	}
	if tenantID != "" {
		where = append(where, "mc.tenant_id = ?")
		args = append(args, tenantID)
	}
	if query.ProviderAccountKey != "" {
		where = append(where, "mc.provider_account_key = ?")
		args = append(args, query.ProviderAccountKey)
	}
	if query.StreamID != "" {
		where = append(where, "mc.stream_id = ?")
		args = append(args, query.StreamID)
	}
	if componentKey != "" {
		where = append(where, "mc.component_key = ?")
		args = append(args, componentKey)
	}
	if query.ComponentKeyHash != "" {
		where = append(where, "mc.component_key_hash = ?")
		args = append(args, query.ComponentKeyHash)
	}
	if query.ItemKind != "" {
		where = append(where, "mc.item_kind = ?")
		args = append(args, query.ItemKind)
	}
	if query.Cursor != "" {
		where = append(where, `(mc.stream_id > ? OR (mc.stream_id = ? AND (mc.sequence > ? OR (mc.sequence = ? AND (mc.observation_id > ? OR (mc.observation_id = ? AND (mc.observation_revision > ? OR (mc.observation_revision = ? AND (mc.item_kind > ? OR (mc.item_kind = ? AND (mc.item_id > ? OR (mc.item_id = ? AND mc.id > ?)))))))))))`)
		args = append(args, position.StreamID, position.StreamID, position.Sequence, position.Sequence, position.ObservationID, position.ObservationID, position.Revision, position.Revision, position.ItemKind, position.ItemKind, position.ItemID, position.ItemID, position.RowID)
	}
	querySQL := `SELECT mc.id, mc.store_id, mc.observation_row_id, mc.observation_id, mc.observation_revision, mc.observation_fingerprint, mc.item_kind, mc.item_id, mc.component_key, mc.component_key_hash, mc.coefficient, mc.scale, mc.value_present, mc.money_present, mc.currency, mc.charge_coverage_json, mc.subject_kind, mc.subject_id, mc.tenant_id, mc.provider_account_key, mc.stream_id, mc.sequence, mc.origin, mc.acquisition, mc.authority, mc.projection_version FROM metering_components mc WHERE ` + strings.Join(where, " AND ") + ` ORDER BY mc.stream_id ASC, mc.sequence ASC, mc.observation_id ASC, mc.observation_revision ASC, mc.item_kind ASC, mc.item_id ASC, mc.id ASC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return ComponentPage{}, fmt.Errorf("metering/journalstore: list components: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]ComponentProjection, 0, limit+1)
	for rows.Next() {
		var item ComponentProjection
		var revision, sequence, scale int64
		var valuePresent, moneyPresent bool
		var subjectKind string
		if err := rows.Scan(&item.ID, &item.StoreID, &item.ObservationRowID, &item.ObservationID, &revision, &item.ObservationFingerprint, &item.ItemKind, &item.ItemID, &item.ComponentKey, &item.ComponentKeyHash, &item.Coefficient, &scale, &valuePresent, &moneyPresent, &item.Currency, &item.ChargeCoverageJSON, &subjectKind, &item.SubjectID, &item.TenantID, &item.ProviderAccountKey, &item.StreamID, &sequence, &item.Origin, &item.Acquisition, &item.Authority, &item.ProjectionVersion); err != nil {
			return ComponentPage{}, fmt.Errorf("metering/journalstore: scan components: %w", err)
		}
		if revision < 0 || sequence < 0 || scale < 0 || scale > math.MaxUint8 {
			return ComponentPage{}, fmt.Errorf("metering/journalstore: invalid component projection integer")
		}
		item.ObservationRevision, item.Sequence, item.Scale = uint64(revision), uint64(sequence), uint8(scale)
		item.ValuePresent, item.MoneyPresent = valuePresent, moneyPresent
		item.SubjectKind = metering.SubjectKind(subjectKind)
		item.CanonicalComponentKey = item.ComponentKey
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ComponentPage{}, err
	}
	page := ComponentPage{Components: make([]ComponentProjection, 0, minInt(len(items), limit))}
	if len(items) > limit {
		last := items[limit-1]
		page.NextCursor = encodeObservationCursor(observationCursor{Version: v2CursorVersion, Kind: "components", ItemKind: last.ItemKind, StoreID: storeID, FilterHash: hash, StreamID: last.StreamID, Sequence: int64(last.Sequence), ObservationID: last.ObservationID, Revision: int64(last.ObservationRevision), ItemID: last.ItemID, RowID: last.ID})
		items = items[:limit]
	}
	page.Components = items
	return page, nil
}

// ListComponents is a compatibility alias for ListObservationComponents.
func (s *DurableStore) ListComponents(ctx context.Context, query ComponentQuery) (ComponentPage, error) {
	return s.ListObservationComponents(ctx, query)
}

// RebuildObservationProjections reconstructs every component row from the
// canonical observation JSON while leaving payload_json and its fingerprint
// untouched.
func (s *DurableStore) RebuildObservationProjections(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("metering/journalstore: nil store")
	}
	if ctx == nil {
		return fmt.Errorf("metering/journalstore: nil context")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("metering/journalstore: rebuild begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.NewRaw(`DELETE FROM metering_components WHERE store_id = ?`, s.cfg.StoreID).Exec(ctx); err != nil {
		return fmt.Errorf("metering/journalstore: rebuild delete projections: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, payload_json, observation_fingerprint FROM metering_facts WHERE store_id = ? AND payload_kind = 'observation' ORDER BY id ASC`, s.cfg.StoreID)
	if err != nil {
		return fmt.Errorf("metering/journalstore: rebuild scan: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var rowID int64
		var payload, storedFingerprint string
		if err := rows.Scan(&rowID, &payload, &storedFingerprint); err != nil {
			return fmt.Errorf("metering/journalstore: rebuild scan row: %w", err)
		}
		var observation metering.Observation
		if err := json.Unmarshal([]byte(payload), &observation); err != nil {
			return fmt.Errorf("metering/journalstore: rebuild decode observation: %w", err)
		}
		canonical, canonicalJSON, err := canonicalObservationForStore(s.cfg.StoreID, observation)
		if err != nil {
			return fmt.Errorf("metering/journalstore: rebuild observation: %w", err)
		}
		if string(canonicalJSON) != payload || (storedFingerprint != "" && storedFingerprint != canonical.Fingerprint()) {
			return fmt.Errorf("%w: canonical observation payload drift for %q revision %d", ErrIdentityCollision, canonical.ID, canonical.Revision)
		}
		if err := insertObservationComponents(ctx, tx, s.cfg.StoreID, rowID, canonical, canonical.Fingerprint()); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("metering/journalstore: rebuild rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("metering/journalstore: rebuild rows close: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("metering/journalstore: rebuild commit: %w", err)
	}
	return nil
}

// RebuildProjections is the short form used by migration/repair callers.
func (s *DurableStore) RebuildProjections(ctx context.Context) error {
	return s.RebuildObservationProjections(ctx)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var (
	_ ObservationTxWriter = (*DurableStore)(nil)
)
