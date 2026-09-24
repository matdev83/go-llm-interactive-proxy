package billingstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun/dialect"
)

// Task 16.2A durable bounded operator readers over existing tables. Readers
// perform no writes and expose no control surface; the cursor authenticator
// key is provisioned by the durable store constructor.
//
// Each reader enforces its trusted scope in SQL and re-checks scope after
// decoding; pages are deterministic with stable authenticated cursors that
// bind the version, query kind, scope/filter and ordering position. The
// complete payload is MACed with a durable server-owned per-store key, so a
// caller holding only cursor contents or other public information cannot
// recompute a position, filter, scope or kind and have it accepted. A legacy
// unauthenticated cursor fails closed.
// Comparison, selection and posting planes travel as independent typed
// fields sourced from their own durable columns; readers never infer one
// plane from another. Missing data yields empty pages or explicit
// missing/incomplete view states, never invented joins or allocations.
// Both dialects share one placeholder SQL shape through Bun.

const (
	operatorCursorPrefix  = "op2."
	operatorCursorVersion = 2
	// operatorCursorKeyBytes is the size of the server-owned HMAC key. It is
	// large enough that guessing or deriving it from public cursor data is
	// infeasible.
	operatorCursorKeyBytes = 32
	// operatorCursorKindAllowance is the bound query kind of the allowance
	// continuation, which wraps an opaque account-window journal position.
	operatorCursorKindAllowance = "allowance"
	// allowanceCursorOrder binds the journal's immutable observed-at history
	// ordering so a future order change invalidates old cursors instead of
	// silently skipping or repeating observations.
	allowanceCursorOrder = "account-window-history-v1"
)

// operatorCursorDomain separates operator-cursor MACs from any other use of
// the same server key (domain separation) and pins the format version.
var operatorCursorDomain = []byte("lip-operator-cursor-v2\x00")

type operatorCursor struct {
	Version       int    `json:"v"`
	Kind          string `json:"kind"`
	StoreID       string `json:"store"`
	Filter        string `json:"filter"`
	CreatedAt     int64  `json:"created_at,omitempty"`
	RecordID      string `json:"record_id,omitempty"`
	RecordVersion int64  `json:"record_version,omitempty"`
	RowID         int64  `json:"row_id,omitempty"`
	LineKey       string `json:"line_key,omitempty"`
	CallID        string `json:"call_id,omitempty"`
	HeadKey       string `json:"head_key,omitempty"`
	Revision      int64  `json:"revision,omitempty"`
	// Order binds the limit-independent ordering definition for kinds whose
	// position is a keyset tuple, so a future ordering change invalidates old
	// cursors instead of silently skipping or repeating rows.
	Order string `json:"order,omitempty"`
	// Snapshot binds the deterministic bounded fingerprint of the durable
	// repeated full-scope fact set (including traversal membership) that an
	// economic-detail continuation was issued against. Recomputing it on the
	// next page and finding a difference rejects the cursor as stale.
	Snapshot string `json:"snapshot,omitempty"`
	// ExtSnapshot binds repeated facts composed above the durable reader (for
	// example resolved statement evidence) into the same authenticated
	// continuation boundary.
	ExtSnapshot string `json:"ext_snapshot,omitempty"`
	// Detail carries the economic-detail observation keyset position.
	Detail *economicDetailCursorPosition `json:"detail,omitempty"`
	// Source carries the opaque inner continuation of a journal-backed reader.
	// It is MACed like every other field, so a caller cannot re-encode an
	// altered source position and have it accepted.
	Source string `json:"source,omitempty"`
}

// economicDetailCursorPosition is the authenticated keyset continuation
// position of one economic-detail observation, mirroring the core
// billing.EconomicObservationPosition contract.
type economicDetailCursorPosition struct {
	StreamID       string `json:"stream_id"`
	Sequence       uint64 `json:"sequence"`
	SourceEventKey string `json:"source_event_key"`
	ObservationID  string `json:"observation_id"`
	Revision       uint64 `json:"revision"`
}

func encodeOperatorCursor(cursor operatorCursor, key []byte) string {
	cursor.Version = operatorCursorVersion
	payload, _ := json.Marshal(cursor)
	tag := operatorCursorMAC(key, payload)
	return operatorCursorPrefix + base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(tag)
}

func decodeOperatorCursor(raw, kind, storeID, filter string, key []byte) (operatorCursor, error) {
	if raw == "" {
		return operatorCursor{}, nil
	}
	// Bound the raw transport before any delimiter split, Base64 decode, HMAC or
	// inner parse, so an oversized caller-controlled payload or tag cannot drive
	// proportional allocation or hashing before rejection.
	if err := economics.ValidateOperatorCursorSize("operator cursor", raw); err != nil {
		return operatorCursor{}, invalidOperatorCursor()
	}
	if !strings.HasPrefix(raw, operatorCursorPrefix) {
		return operatorCursor{}, invalidOperatorCursor()
	}
	segments := strings.SplitN(strings.TrimPrefix(raw, operatorCursorPrefix), ".", 3)
	if len(segments) != 2 || segments[0] == "" || segments[1] == "" {
		return operatorCursor{}, invalidOperatorCursor()
	}
	payload, err := base64.RawURLEncoding.DecodeString(segments[0])
	if err != nil {
		return operatorCursor{}, invalidOperatorCursor()
	}
	tag, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		return operatorCursor{}, invalidOperatorCursor()
	}
	if len(key) == 0 || !hmac.Equal(tag, operatorCursorMAC(key, payload)) {
		return operatorCursor{}, invalidOperatorCursor()
	}
	var cursor operatorCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return operatorCursor{}, invalidOperatorCursor()
	}
	if cursor.Version != operatorCursorVersion || cursor.Kind != kind || cursor.StoreID != storeID || cursor.Filter != filter {
		return operatorCursor{}, invalidOperatorCursor()
	}
	switch kind {
	case "discrepancies":
		if cursor.RecordID == "" || cursor.RecordVersion <= 0 || cursor.RowID <= 0 {
			return operatorCursor{}, invalidOperatorCursor()
		}
	case "statement-lines":
		if cursor.LineKey == "" {
			return operatorCursor{}, invalidOperatorCursor()
		}
	case "adjustments":
		if cursor.CallID == "" || cursor.HeadKey == "" || cursor.RowID <= 0 {
			return operatorCursor{}, invalidOperatorCursor()
		}
	case "economic-detail":
		if cursor.Order != billing.EconomicDetailObservationOrder || cursor.Detail == nil {
			return operatorCursor{}, invalidOperatorCursor()
		}
		if cursor.Detail.StreamID == "" || cursor.Detail.ObservationID == "" || cursor.Detail.Revision == 0 {
			return operatorCursor{}, invalidOperatorCursor()
		}
	case operatorCursorKindAllowance:
		if cursor.Order != allowanceCursorOrder || cursor.Source == "" {
			return operatorCursor{}, invalidOperatorCursor()
		}
	default:
		return operatorCursor{}, invalidOperatorCursor()
	}
	canonical, _ := json.Marshal(cursor)
	if base64.RawURLEncoding.EncodeToString(canonical) != segments[0] {
		return operatorCursor{}, invalidOperatorCursor()
	}
	return cursor, nil
}

// invalidOperatorCursor returns the single stable classified error for every
// cursor rejection. It deliberately carries no MAC, key, payload or position
// detail so callers cannot use error contents as an oracle.
func invalidOperatorCursor() error {
	return fmt.Errorf("%w: cursor rejected", economics.ErrOperatorCursorInvalid)
}

// operatorCursorMAC authenticates the complete canonical cursor payload under
// a domain-separated HMAC-SHA-256.
func operatorCursorMAC(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(operatorCursorDomain)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func operatorFilterHash(value any) string {
	payload, _ := json.Marshal(value)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// reconciliationAccountExpr extracts authoritative account ownership from the
// stored reconciliation subject payload. SQLite reads the JSON document
// directly; PostgreSQL casts the TEXT column (the writer JSON is always valid,
// and a cast failure fails the scope query closed). A missing account key
// coalesces to empty, which never equals a validated non-empty requested
// account, so account-less rows are excluded rather than implicitly matched.
func reconciliationAccountExpr(pg bool) string {
	if pg {
		return `COALESCE(subject_json::jsonb ->> 'account_id', '')`
	}
	return `COALESCE(json_extract(subject_json, '$.account_id'), '')`
}

// QueryDiscrepancies returns bounded retained-reconciliation summaries in
// durable order. The requested account is enforced as an authoritative SQL
// predicate before ordering and LIMIT, so foreign or account-less retained
// evidence cannot leak into an account-only page, deny it, or shift its
// pagination. Rows parse through the canonical retention validator, so a
// corrupt payload fails closed instead of surfacing partial views.
func (s *DurableStore) QueryDiscrepancies(ctx context.Context, query economics.DiscrepancyQuery) (economics.DiscrepancyPage, error) {
	if err := s.validateContext(ctx); err != nil {
		return economics.DiscrepancyPage{}, err
	}
	normalized, err := query.Normalize()
	if err != nil {
		return economics.DiscrepancyPage{}, err
	}
	if normalized.Scope.StoreID != s.storeID {
		return economics.DiscrepancyPage{}, fmt.Errorf("%w: discrepancy store scope", economics.ErrOperatorScopeMismatch)
	}
	filter := struct {
		Store, Tenant, Account, Kind, ID string
	}{normalized.Scope.StoreID, normalized.Scope.TenantID, normalized.Scope.AccountID, string(normalized.SubjectKind), normalized.SubjectID}
	hash := operatorFilterHash(filter)
	position, err := decodeOperatorCursor(normalized.Cursor, "discrepancies", s.storeID, hash, s.cursorKey)
	if err != nil {
		return economics.DiscrepancyPage{}, err
	}
	where := []string{`store_id = ?`, `result_schema_version = ?`}
	args := []any{s.storeID, ReconciliationRecordSchemaRetention}
	if normalized.Scope.TenantID != "" {
		where = append(where, `tenant_id = ?`)
		args = append(args, normalized.Scope.TenantID)
	}
	if normalized.Scope.AccountID != "" {
		where = append(where, reconciliationAccountExpr(s.db.Dialect().Name() == dialect.PG)+` = ?`)
		args = append(args, normalized.Scope.AccountID)
	}
	if normalized.SubjectKind != "" {
		where = append(where, `subject_kind = ?`, `subject_id = ?`)
		args = append(args, string(normalized.SubjectKind), normalized.SubjectID)
	}
	if normalized.Cursor != "" {
		where = append(where, `(created_at_unix > ? OR (created_at_unix = ? AND (reconciliation_id > ? OR (reconciliation_id = ? AND (reconciliation_version > ? OR (reconciliation_version = ? AND id > ?))))))`)
		args = append(args, position.CreatedAt, position.CreatedAt, position.RecordID, position.RecordID, position.RecordVersion, position.RecordVersion, position.RowID)
	}
	var rows []struct {
		RecordID            string `bun:"reconciliation_id"`
		Version             int64  `bun:"reconciliation_version"`
		SubjectJSON         string `bun:"subject_json"`
		Scope               string `bun:"scope"`
		Basis               string `bun:"basis"`
		ResultSchemaVersion int64  `bun:"result_schema_version"`
		InputSetHash        string `bun:"input_set_hash"`
		LocalInputHash      string `bun:"local_input_hash"`
		ProviderInputHash   string `bun:"provider_input_hash"`
		PolicyID            string `bun:"policy_id"`
		PolicyVersion       string `bun:"policy_version"`
		ResultJSON          string `bun:"result_json"`
		CreatedAt           int64  `bun:"created_at_unix"`
		RowID               int64  `bun:"id"`
	}
	if err := s.db.NewRaw(`SELECT reconciliation_id, reconciliation_version, subject_json, scope, basis, result_schema_version, `+
		`input_set_hash, local_input_hash, provider_input_hash, policy_id, policy_version, result_json, created_at_unix, id `+
		`FROM billing_reconciliations WHERE `+
		strings.Join(where, ` AND `)+` ORDER BY created_at_unix ASC, reconciliation_id ASC, reconciliation_version ASC, id ASC LIMIT ?`,
		append(args, normalized.Limit+1)...).Scan(ctx, &rows); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return economics.DiscrepancyPage{}, fmt.Errorf("billingstore: discrepancy rows: %w", err)
		}
	}
	page := economics.DiscrepancyPage{}
	if len(rows) > normalized.Limit {
		last := rows[normalized.Limit-1]
		page.NextCursor = encodeOperatorCursor(operatorCursor{
			Kind: "discrepancies", StoreID: s.storeID, Filter: hash,
			CreatedAt: last.CreatedAt, RecordID: last.RecordID, RecordVersion: last.Version, RowID: last.RowID,
		}, s.cursorKey)
		rows = rows[:normalized.Limit]
	}
	for _, row := range rows {
		var subject metering.SubjectRef
		if err := json.Unmarshal([]byte(row.SubjectJSON), &subject); err != nil {
			return economics.DiscrepancyPage{}, fmt.Errorf("billingstore: discrepancy record mismatch")
		}
		result, err := reconciliationRetentionFromRecord(ReconciliationRecord{
			ID: row.RecordID, Version: uint64(row.Version), Subject: subject, Scope: row.Scope,
			Basis: economics.ValuationBasis(row.Basis), ResultSchemaVersion: uint32(row.ResultSchemaVersion),
			InputSetHash: row.InputSetHash, LocalInputHash: row.LocalInputHash, ProviderInputHash: row.ProviderInputHash,
			PolicyID: row.PolicyID, PolicyVersion: row.PolicyVersion,
			ResultJSON: json.RawMessage(row.ResultJSON), CreatedAt: time.Unix(0, row.CreatedAt).UTC(),
		})
		if err != nil {
			return economics.DiscrepancyPage{}, fmt.Errorf("billingstore: discrepancy record mismatch")
		}
		view, err := discrepancyViewFromRetention(result)
		if err != nil {
			return economics.DiscrepancyPage{}, err
		}
		if normalized.Scope.AccountID != "" && view.Subject.AccountID != normalized.Scope.AccountID {
			return economics.DiscrepancyPage{}, fmt.Errorf("%w: discrepancy account scope", economics.ErrOperatorScopeMismatch)
		}
		page.Items = append(page.Items, view)
	}
	return page, nil
}

func discrepancyViewFromRetention(result billing.ReconciliationRetentionResult) (economics.DiscrepancyView, error) {
	view := economics.DiscrepancyView{
		ID: result.ID, Revision: result.ResultRevision, Subject: result.Subject.Clone(),
		Scope: result.Scope, Payer: result.Payer,
		PolicyID: result.Policy.ID, PolicyVersion: result.Policy.Version,
		InputSetHash:    result.InputSetHash,
		ValuationIDs:    append([]string(nil), result.ValuationIDs...),
		ObservationRefs: append([]metering.ObservationRef(nil), result.ObservationRefs...),
		CreatedAt:       result.CreatedAt,
	}
	if result.Quantity != nil {
		status := economics.DiscrepancyStatus(result.Quantity.Status)
		if !status.IsKnown() {
			return economics.DiscrepancyView{}, fmt.Errorf("%w: retained quantity status %q", economics.ErrOperatorQueryInvalid, result.Quantity.Status)
		}
		view.QuantityStatus = status
		view.QuantityComplete = result.Quantity.Complete
	}
	if result.Monetary != nil {
		state := economics.MonetaryState(result.Monetary.Status)
		if !state.IsKnown() {
			return economics.DiscrepancyView{}, fmt.Errorf("%w: retained monetary state %q", economics.ErrOperatorQueryInvalid, result.Monetary.Status)
		}
		reason := economics.DiscrepancyReason(result.Monetary.Reason)
		if !reason.IsKnown() {
			return economics.DiscrepancyView{}, fmt.Errorf("%w: retained monetary reason %q", economics.ErrOperatorQueryInvalid, result.Monetary.Reason)
		}
		view.MonetaryState = state
		view.MonetaryReason = reason
	}
	if result.Aggregate != nil {
		aggregate, err := discrepancyAggregateFromRetention(*result.Aggregate)
		if err != nil {
			return economics.DiscrepancyView{}, err
		}
		view.Aggregate = aggregate
	}
	for _, diagnostic := range result.Diagnostics {
		reason := economics.DiscrepancyReason(diagnostic.Reason)
		if !reason.IsKnown() {
			return economics.DiscrepancyView{}, fmt.Errorf("%w: retained diagnostic reason %q", economics.ErrOperatorQueryInvalid, diagnostic.Reason)
		}
		view.Diagnostics = append(view.Diagnostics, economics.DiscrepancyDiagnostic{Code: diagnostic.Code, Reason: reason, Detail: diagnostic.Detail})
	}
	if err := view.Validate(); err != nil {
		return economics.DiscrepancyView{}, err
	}
	return view, nil
}

// discrepancyAggregateFromRetention projects one retained aggregate plane into
// the bounded public operator DTO. The classification distribution is folded
// into a single most-severe status and a completeness flag, and the retained
// missing/incomparable/conflict finding identities are preserved verbatim and
// canonically deduplicated. The aggregate is an independent plane: it is never
// coerced into a quantity or monetary status and never carries a monetary
// amount.
func discrepancyAggregateFromRetention(in billing.ReconciliationAggregate) (*economics.DiscrepancyAggregate, error) {
	out := &economics.DiscrepancyAggregate{}
	counts := make(map[economics.DiscrepancyStatus]int)
	for i := range in.Rows {
		row := in.Rows[i]
		out.AffectedCount += row.AffectedCount
		out.MissingIDs = append(out.MissingIDs, row.MissingIDs...)
		out.IncomparableIDs = append(out.IncomparableIDs, row.IncomparableIDs...)
		out.ConflictIDs = append(out.ConflictIDs, row.ConflictIDs...)
		for _, count := range row.StatusCounts {
			status := economics.DiscrepancyStatus(count.Status)
			if !status.IsKnown() {
				return nil, fmt.Errorf("%w: retained aggregate status %q", economics.ErrOperatorQueryInvalid, count.Status)
			}
			counts[status] += count.Count
		}
	}
	out.MissingIDs = canonicalDiscrepancyAggregateIDs(out.MissingIDs)
	out.IncomparableIDs = canonicalDiscrepancyAggregateIDs(out.IncomparableIDs)
	out.ConflictIDs = canonicalDiscrepancyAggregateIDs(out.ConflictIDs)
	out.Status = discrepancyAggregateSummaryStatus(counts)
	out.Complete = discrepancyAggregateComplete(out.Status, out.MissingIDs, out.IncomparableIDs, out.ConflictIDs)
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

// discrepancyAggregateSummaryStatus selects the most severe retained
// classification so the bounded summary cannot hide unresolved evidence.
func discrepancyAggregateSummaryStatus(counts map[economics.DiscrepancyStatus]int) economics.DiscrepancyStatus {
	for _, status := range []economics.DiscrepancyStatus{
		economics.DiscrepancyConflict, economics.DiscrepancyIncomparable,
		economics.DiscrepancyMissingProvider, economics.DiscrepancyMissingLocal,
		economics.DiscrepancyPartial, economics.DiscrepancyDiscrepant,
		economics.DiscrepancyWithinTolerance, economics.DiscrepancyMatched,
	} {
		if counts[status] > 0 {
			return status
		}
	}
	return ""
}

// discrepancyAggregateComplete reports whether the aggregate retains only
// fully classified comparable evidence. An empty aggregate (no row at all) is
// complete: it carries no unresolved classification.
func discrepancyAggregateComplete(status economics.DiscrepancyStatus, missing, incomparable, conflict []string) bool {
	if len(missing) > 0 || len(incomparable) > 0 || len(conflict) > 0 {
		return false
	}
	switch status {
	case "", economics.DiscrepancyMatched, economics.DiscrepancyWithinTolerance, economics.DiscrepancyDiscrepant:
		return true
	default:
		return false
	}
}

// canonicalDiscrepancyAggregateIDs returns a sorted, de-duplicated copy so the
// same retained evidence always projects to one deterministic wire identity.
func canonicalDiscrepancyAggregateIDs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	deduped := out[:0]
	for i, value := range out {
		if i > 0 && value == out[i-1] {
			continue
		}
		deduped = append(deduped, value)
	}
	return deduped
}

// AccountWindowSource is the narrow gauge-history port implemented by the
// metering journal. The billingstore allowance adapter owns scope
// validation and public DTO mapping; SQL stays in the journal.
type AccountWindowSource interface {
	ListAccountWindowObservations(context.Context, coremetering.AccountWindowQuery) (coremetering.AccountWindowObservationPage, error)
}

// OperatorCursorAuthority authenticates and seals operator-reader
// continuation cursors under one durable store's server-owned key. It exists
// so a journal-backed operator reader can bind its continuation to the same
// single key authority as billingstore-native readers without exposing key
// material or introducing a second secret.
type OperatorCursorAuthority interface {
	// DecodeAllowanceCursor authenticates the complete operator-bound cursor
	// before parsing it and returns the opaque inner journal continuation. A
	// legacy, tampered, replayed or cross-scope cursor fails closed with the
	// bounded operator invalid-cursor classification.
	DecodeAllowanceCursor(raw, storeID, filter string) (string, error)
	// EncodeAllowanceCursor binds an opaque inner journal continuation under
	// the allowance kind, the authorized store/filter scope and the journal
	// history order.
	EncodeAllowanceCursor(storeID, filter, source string) string
}

var _ OperatorCursorAuthority = (*DurableStore)(nil)

// DecodeAllowanceCursor implements OperatorCursorAuthority for the durable
// billing store that owns the cursor key.
func (s *DurableStore) DecodeAllowanceCursor(raw, storeID, filter string) (string, error) {
	cursor, err := decodeOperatorCursor(raw, operatorCursorKindAllowance, storeID, filter, s.cursorKey)
	if err != nil {
		return "", err
	}
	return cursor.Source, nil
}

// EncodeAllowanceCursor implements OperatorCursorAuthority for the durable
// billing store that owns the cursor key.
func (s *DurableStore) EncodeAllowanceCursor(storeID, filter, source string) string {
	return encodeOperatorCursor(operatorCursor{
		Kind: operatorCursorKindAllowance, StoreID: storeID, Filter: filter,
		Order: allowanceCursorOrder, Source: source,
	}, s.cursorKey)
}

// allowanceOperatorFilter binds the complete authorized allowance scope and
// refinements. The customer account scope is never present: Normalize rejects
// account-scoped allowance history.
func allowanceOperatorFilter(query economics.AllowanceQuery) string {
	return operatorFilterHash(struct {
		Store, Tenant, Provider, Pool, Window string
	}{
		query.Scope.StoreID, query.Scope.TenantID, query.ProviderAccountKey, query.PoolID, query.WindowID,
	})
}

// AllowanceReader projects provider allowance gauge history through the
// public operator DTOs. Gauges pass through verbatim: no summation, no
// inferred per-request debit.
type AllowanceReader struct {
	Source AccountWindowSource
	// Authority is the durable operator-cursor authenticator. It must be the
	// same key authority that protects every other operator cursor; a reader
	// without it fails closed on any continuation.
	Authority OperatorCursorAuthority
}

var _ economics.AllowanceReader = AllowanceReader{}

// QueryAllowances returns one bounded gauge-history page. The operator
// continuation is authenticated before the inner journal position is trusted
// or parsed, binds kind, full authorized scope, filters and journal order, and
// is re-sealed as an operator cursor; scope is re-checked after projection.
func (r AllowanceReader) QueryAllowances(ctx context.Context, query economics.AllowanceQuery) (economics.AllowancePage, error) {
	if r.Source == nil {
		return economics.AllowancePage{}, fmt.Errorf("%w: allowance source unavailable", economics.ErrOperatorQueryInvalid)
	}
	normalized, err := query.Normalize()
	if err != nil {
		return economics.AllowancePage{}, err
	}
	if ctx == nil {
		return economics.AllowancePage{}, fmt.Errorf("%w: nil context", economics.ErrOperatorQueryInvalid)
	}
	if err := ctx.Err(); err != nil {
		return economics.AllowancePage{}, fmt.Errorf("%w: %v", economics.ErrOperatorQueryInvalid, err)
	}
	filter := allowanceOperatorFilter(normalized)
	innerCursor := ""
	if normalized.Cursor != "" {
		if r.Authority == nil {
			return economics.AllowancePage{}, invalidOperatorCursor()
		}
		innerCursor, err = r.Authority.DecodeAllowanceCursor(normalized.Cursor, normalized.Scope.StoreID, filter)
		if err != nil {
			return economics.AllowancePage{}, err
		}
	}
	source, err := r.Source.ListAccountWindowObservations(ctx, coremetering.AccountWindowQuery{
		StoreID: normalized.Scope.StoreID, TenantID: normalized.Scope.TenantID,
		ProviderAccountKey: normalized.ProviderAccountKey, PoolID: normalized.PoolID, WindowID: normalized.WindowID,
		Limit: normalized.Limit, Cursor: innerCursor,
	})
	if err != nil {
		return economics.AllowancePage{}, err
	}
	page := economics.AllowancePage{}
	if source.NextCursor != "" {
		if r.Authority == nil {
			return economics.AllowancePage{}, fmt.Errorf("%w: allowance cursor authority unavailable", economics.ErrOperatorQueryInvalid)
		}
		page.NextCursor = r.Authority.EncodeAllowanceCursor(normalized.Scope.StoreID, filter, source.NextCursor)
	}
	for _, observation := range source.Observations {
		if observation.Subject.StoreID != normalized.Scope.StoreID {
			return economics.AllowancePage{}, fmt.Errorf("%w: allowance store scope", economics.ErrOperatorScopeMismatch)
		}
		if normalized.Scope.TenantID != "" && observation.Subject.TenantID != "" && observation.Subject.TenantID != normalized.Scope.TenantID {
			return economics.AllowancePage{}, fmt.Errorf("%w: allowance tenant scope", economics.ErrOperatorScopeMismatch)
		}
		page.Observations = append(page.Observations, observation.Clone())
	}
	if err := page.Validate(); err != nil {
		return economics.AllowancePage{}, err
	}
	return page, nil
}

type statementLineDetailRow struct {
	LineKey             string `bun:"line_key"`
	StatementID         string `bun:"statement_id"`
	PeriodID            string `bun:"period_id"`
	ProviderAccountKey  string `bun:"provider_account_key"`
	TenantID            string `bun:"tenant_id"`
	LineID              string `bun:"line_id"`
	LineRevision        int64  `bun:"line_revision"`
	Outcome             string `bun:"outcome"`
	ChargeItemID        string `bun:"charge_item_id"`
	ObservationID       string `bun:"observation_id"`
	ObservationRevision int64  `bun:"observation_revision"`
	Payload             string `bun:"payload_json"`
}

// QueryStatementLines returns bounded retained statement lines in line-key
// order over the existing scope index. Unmatched aggregate lines keep their
// statement/period scope and never gain a request linkage.
func (s *DurableStore) QueryStatementLines(ctx context.Context, query economics.StatementLineQuery) (economics.StatementLinePage, error) {
	if err := s.validateContext(ctx); err != nil {
		return economics.StatementLinePage{}, err
	}
	normalized, err := query.Normalize()
	if err != nil {
		return economics.StatementLinePage{}, err
	}
	if normalized.Scope.StoreID != s.storeID {
		return economics.StatementLinePage{}, fmt.Errorf("%w: statement store scope", economics.ErrOperatorScopeMismatch)
	}
	filter := struct {
		Store, Tenant, Provider, Statement, Period, Line, Outcome string
	}{normalized.Scope.StoreID, normalized.Scope.TenantID, normalized.ProviderAccountKey, normalized.StatementID, normalized.PeriodID, normalized.LineID, string(normalized.Outcome)}
	hash := operatorFilterHash(filter)
	position, err := decodeOperatorCursor(normalized.Cursor, "statement-lines", s.storeID, hash, s.cursorKey)
	if err != nil {
		return economics.StatementLinePage{}, err
	}
	where := []string{`store_id = ?`, `tenant_id = ?`}
	args := []any{s.storeID, normalized.Scope.TenantID}
	if normalized.ProviderAccountKey != "" {
		where = append(where, `provider_account_key = ?`)
		args = append(args, normalized.ProviderAccountKey)
	}
	if normalized.StatementID != "" {
		where = append(where, `statement_id = ?`)
		args = append(args, normalized.StatementID)
	}
	if normalized.PeriodID != "" {
		where = append(where, `period_id = ?`)
		args = append(args, normalized.PeriodID)
	}
	if normalized.LineID != "" {
		where = append(where, `line_id = ?`)
		args = append(args, normalized.LineID)
	}
	if normalized.Outcome != "" {
		where = append(where, `outcome = ?`)
		args = append(args, string(normalized.Outcome))
	}
	if normalized.Cursor != "" {
		where = append(where, `line_key > ?`)
		args = append(args, position.LineKey)
	}
	var rows []statementLineDetailRow
	if err := s.db.NewRaw(`SELECT line_key, statement_id, period_id, provider_account_key, tenant_id, line_id, line_revision, outcome, charge_item_id, observation_id, observation_revision, payload_json FROM billing_statement_lines WHERE `+
		strings.Join(where, ` AND `)+` ORDER BY line_key ASC LIMIT ?`,
		append(args, normalized.Limit+1)...).Scan(ctx, &rows); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return economics.StatementLinePage{}, fmt.Errorf("billingstore: statement line rows: %w", err)
		}
	}
	page := economics.StatementLinePage{}
	if len(rows) > normalized.Limit {
		page.NextCursor = encodeOperatorCursor(operatorCursor{
			Kind: "statement-lines", StoreID: s.storeID, Filter: hash, LineKey: rows[normalized.Limit-1].LineKey,
		}, s.cursorKey)
		rows = rows[:normalized.Limit]
	}
	for _, row := range rows {
		var line economics.StatementLine
		if err := json.Unmarshal([]byte(row.Payload), &line); err != nil {
			return economics.StatementLinePage{}, fmt.Errorf("billingstore: statement line record mismatch")
		}
		if err := line.Validate(s.storeID); err != nil {
			return economics.StatementLinePage{}, fmt.Errorf("billingstore: statement line record mismatch")
		}
		if line.ID != row.LineID || line.Subject.StatementID != row.StatementID || string(line.Outcome) != row.Outcome ||
			line.ChargeItemID != row.ChargeItemID || line.Observation.ObservationID != row.ObservationID {
			return economics.StatementLinePage{}, fmt.Errorf("billingstore: statement line record mismatch")
		}
		view := economics.StatementLineView{
			StatementID: row.StatementID, LineID: row.LineID, Revision: uint64(row.LineRevision),
			PeriodID: row.PeriodID, ProviderAccountKey: row.ProviderAccountKey,
			Outcome: line.Outcome, UnmatchedReason: line.UnmatchedReason,
			ChargeItemID: line.ChargeItemID, Observation: line.Observation,
		}
		if err := view.Validate(); err != nil {
			return economics.StatementLinePage{}, err
		}
		page.Lines = append(page.Lines, view)
	}
	return page, nil
}

type adjustmentDetailRow struct {
	OperationKey         string `bun:"operation_key"`
	LinkKey              string `bun:"link_key"`
	Fingerprint          string `bun:"fingerprint"`
	AccountID            string `bun:"account_id"`
	CallID               string `bun:"call_id"`
	HeadKey              string `bun:"head_key"`
	Status               string `bun:"status"`
	Comparison           string `bun:"comparison"`
	Posting              string `bun:"posting"`
	PreviousValuationID  string `bun:"previous_valuation_id"`
	PreviousRevision     int64  `bun:"previous_revision"`
	PreviousInputSetHash string `bun:"previous_input_set_hash"`
	CurrentValuationID   string `bun:"current_valuation_id"`
	CurrentRevision      int64  `bun:"current_revision"`
	CurrentInputSetHash  string `bun:"current_input_set_hash"`
	Currency             string `bun:"currency"`
	DeltaJSON            string `bun:"delta_json"`
	JournalTransactionID string `bun:"journal_transaction_id"`
	CreatedAt            int64  `bun:"created_at_unix"`
	AdjustmentRevision   int64  `bun:"adjustment_revision"`
	RowID                int64  `bun:"row_id"`
	HeadSelectionStatus  string `bun:"head_selection_status"`
	HeadSelectionReason  string `bun:"head_selection_reason"`
	HeadSubjectTenant    string `bun:"head_subject_tenant"`
}

// QueryAdjustments returns bounded immutable corrections in
// (call, head, revision) order with one indexed join to the head for the
// latest frozen selection plane. Status, comparison, posting and selection
// come from their own durable columns and are never inferred from each
// other. Signed deltas (including downward corrections) pass through
// exactly; no-op transitions carry no delta.
func (s *DurableStore) QueryAdjustments(ctx context.Context, query economics.AdjustmentQuery) (economics.AdjustmentPage, error) {
	if err := s.validateContext(ctx); err != nil {
		return economics.AdjustmentPage{}, err
	}
	normalized, err := query.Normalize()
	if err != nil {
		return economics.AdjustmentPage{}, err
	}
	if normalized.Scope.StoreID != s.storeID {
		return economics.AdjustmentPage{}, fmt.Errorf("%w: adjustment store scope", economics.ErrOperatorScopeMismatch)
	}
	filter := struct {
		Store, Account, Call, Head string
	}{normalized.Scope.StoreID, normalized.Scope.AccountID, normalized.CallID, normalized.HeadKey}
	hash := operatorFilterHash(filter)
	position, err := decodeOperatorCursor(normalized.Cursor, "adjustments", s.storeID, hash, s.cursorKey)
	if err != nil {
		return economics.AdjustmentPage{}, err
	}
	where := []string{`a.store_id = ?`, `a.account_id = ?`}
	args := []any{s.storeID, normalized.Scope.AccountID}
	if normalized.CallID != "" {
		where = append(where, `a.call_id = ?`)
		args = append(args, normalized.CallID)
	}
	if normalized.HeadKey != "" {
		where = append(where, `a.head_key = ?`)
		args = append(args, normalized.HeadKey)
	}
	if normalized.Cursor != "" {
		where = append(where, `(a.call_id > ? OR (a.call_id = ? AND (a.head_key > ? OR (a.head_key = ? AND (a.adjustment_revision > ? OR (a.adjustment_revision = ? AND a.id > ?))))))`)
		args = append(args, position.CallID, position.CallID, position.HeadKey, position.HeadKey, position.Revision, position.Revision, position.RowID)
	}
	var rows []adjustmentDetailRow
	if err := s.db.NewRaw(`SELECT a.operation_key, a.link_key, a.fingerprint, a.account_id, a.call_id, a.head_key, a.status, a.comparison, a.posting, `+
		`a.previous_valuation_id, a.previous_revision, a.previous_input_set_hash, a.current_valuation_id, a.current_revision, a.current_input_set_hash, `+
		`a.currency, a.delta_json, a.journal_transaction_id, a.created_at_unix, a.adjustment_revision, a.id AS row_id, `+
		`h.selection_status AS head_selection_status, h.selection_reason AS head_selection_reason, h.subject_json AS head_subject_tenant `+
		`FROM billing_selected_cost_adjustments a LEFT JOIN billing_provider_cost_heads h `+
		`ON h.store_id = a.store_id AND h.account_id = a.account_id AND h.call_id = a.call_id AND h.head_key = a.head_key `+
		`WHERE `+strings.Join(where, ` AND `)+` ORDER BY a.call_id ASC, a.head_key ASC, a.adjustment_revision ASC, a.id ASC LIMIT ?`,
		append(args, normalized.Limit+1)...).Scan(ctx, &rows); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return economics.AdjustmentPage{}, fmt.Errorf("billingstore: adjustment rows: %w", err)
		}
	}
	page := economics.AdjustmentPage{}
	if len(rows) > normalized.Limit {
		last := rows[normalized.Limit-1]
		page.NextCursor = encodeOperatorCursor(operatorCursor{
			Kind: "adjustments", StoreID: s.storeID, Filter: hash,
			CallID: last.CallID, HeadKey: last.HeadKey, Revision: last.AdjustmentRevision, RowID: last.RowID,
		}, s.cursorKey)
		rows = rows[:normalized.Limit]
	}
	for _, row := range rows {
		view, err := adjustmentViewFromRow(row, normalized.Scope.TenantID)
		if err != nil {
			return economics.AdjustmentPage{}, err
		}
		page.Adjustments = append(page.Adjustments, view)
	}
	return page, nil
}

func adjustmentViewFromRow(row adjustmentDetailRow, scopeTenant string) (economics.AdjustmentView, error) {
	status := economics.AdjustmentTransitionStatus(row.Status)
	if !status.IsKnown() {
		return economics.AdjustmentView{}, fmt.Errorf("%w: retained adjustment status %q", economics.ErrOperatorQueryInvalid, row.Status)
	}
	comparison := economics.AdjustmentComparison(row.Comparison)
	if !comparison.IsKnown() {
		return economics.AdjustmentView{}, fmt.Errorf("%w: retained adjustment comparison %q", economics.ErrOperatorQueryInvalid, row.Comparison)
	}
	posting := economics.AdjustmentPosting(row.Posting)
	if !posting.IsKnown() {
		return economics.AdjustmentView{}, fmt.Errorf("%w: retained adjustment posting %q", economics.ErrOperatorQueryInvalid, row.Posting)
	}
	selection := economics.AdjustmentSelection(row.HeadSelectionStatus)
	if !selection.IsKnown() {
		return economics.AdjustmentView{}, fmt.Errorf("%w: retained head selection %q", economics.ErrOperatorQueryInvalid, row.HeadSelectionStatus)
	}
	if _, err := billing.ParseBillingCallID(row.CallID); err != nil {
		return economics.AdjustmentView{}, fmt.Errorf("billingstore: adjustment record mismatch")
	}
	headTenant, err := headSubjectTenant(row.HeadSubjectTenant)
	if err != nil {
		return economics.AdjustmentView{}, fmt.Errorf("billingstore: adjustment record mismatch")
	}
	if scopeTenant != "" && headTenant != "" && headTenant != scopeTenant {
		return economics.AdjustmentView{}, fmt.Errorf("%w: adjustment tenant scope", economics.ErrOperatorScopeMismatch)
	}
	view := economics.AdjustmentView{
		OperationKey: row.OperationKey, LinkKey: row.LinkKey, Fingerprint: row.Fingerprint,
		AccountID: row.AccountID, CallID: row.CallID, HeadKey: row.HeadKey,
		Status: status, Comparison: comparison, Posting: posting,
		SelectionStatus: selection, SelectionReason: row.HeadSelectionReason,
		Currency: row.Currency, JournalTransactionID: row.JournalTransactionID,
		CreatedAt: time.Unix(0, row.CreatedAt).UTC(),
	}
	if row.PreviousValuationID != "" {
		if row.PreviousRevision <= 0 {
			return economics.AdjustmentView{}, fmt.Errorf("billingstore: adjustment record mismatch")
		}
		previous := economics.AdjustmentValuationRef{
			ValuationID: row.PreviousValuationID, Revision: uint64(row.PreviousRevision), InputSetHash: row.PreviousInputSetHash,
		}
		view.Previous = &previous
	}
	view.Current = economics.AdjustmentValuationRef{
		ValuationID: row.CurrentValuationID, Revision: uint64(row.CurrentRevision), InputSetHash: row.CurrentInputSetHash,
	}
	delta, err := adjustmentDeltaFromJSON(row.DeltaJSON)
	if err != nil {
		return economics.AdjustmentView{}, err
	}
	view.Delta = delta
	if err := view.Validate(); err != nil {
		return economics.AdjustmentView{}, err
	}
	return view, nil
}

func headSubjectTenant(subjectJSON string) (string, error) {
	if strings.TrimSpace(subjectJSON) == "" {
		return "", nil
	}
	var subject metering.SubjectRef
	if err := json.Unmarshal([]byte(subjectJSON), &subject); err != nil {
		return "", err
	}
	return subject.TenantID, nil
}

func adjustmentDeltaFromJSON(raw string) (*economics.ExactAmount, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var stored billing.MonetaryExactAmount
	if err := json.Unmarshal([]byte(trimmed), &stored); err != nil {
		return nil, fmt.Errorf("billingstore: adjustment record mismatch")
	}
	amount := economics.ExactAmount{Currency: stored.Currency, Numerator: stored.Numerator, Denominator: stored.Denominator}
	if stored.Decimal != nil {
		decimal := *stored.Decimal
		amount.Decimal = &decimal
	}
	if err := amount.Validate(); err != nil {
		return nil, fmt.Errorf("billingstore: adjustment record mismatch")
	}
	return &amount, nil
}
