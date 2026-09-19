package billing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 12.3B core-side retention contract. It defines the canonical,
// versioned, bounded full-result payload that infrastructure persists. It
// contains no SQL or persistence dependency.

var (
	// ErrInvalidReconciliationRetention identifies a malformed, non-canonical
	// or oversized durable reconciliation result.
	ErrInvalidReconciliationRetention = errors.New("billing: invalid reconciliation retention result")
)

const (
	// ReconciliationRetentionSchemaVersionV1 is the first durable result
	// schema version.
	ReconciliationRetentionSchemaVersionV1 uint32 = 1
	// MaxReconciliationRetentionBytes bounds the canonical durable payload.
	MaxReconciliationRetentionBytes = 1 << 20
	// MaxReconciliationRetentionObservationRefs bounds retained source refs.
	MaxReconciliationRetentionObservationRefs = 4096
	// MaxReconciliationRetentionValuations bounds retained valuation ids.
	MaxReconciliationRetentionValuations = 128
	// MaxReconciliationRetentionDiagnostics bounds retained diagnostics.
	MaxReconciliationRetentionDiagnostics = 64
	// MaxReconciliationRetentionDiagnosticBytes bounds one diagnostic detail.
	MaxReconciliationRetentionDiagnosticBytes = 512
)

// ReconciliationRetentionDiagnostic is one safe, bounded operational signal.
// It never contains prompts, media, credentials or raw provider payloads.
type ReconciliationRetentionDiagnostic struct {
	Code   string                         `json:"code"`
	Reason ReconciliationComparisonReason `json:"reason,omitempty"`
	Detail string                         `json:"detail,omitempty"`
}

// ReconciliationRetentionResult is the canonical full reconciliation result:
// identity/revision, economic scope, policy version, comparison input hashes
// and refs, the 12.1 quantity comparison, the 12.2 monetary decomposition with
// its alternative valuations, the 12.3 aggregate projection and safe
// diagnostics. Exact decimals and source refs are preserved verbatim.
type ReconciliationRetentionResult struct {
	SchemaVersion  uint32                       `json:"schema_version"`
	ID             string                       `json:"id"`
	ResultRevision uint64                       `json:"result_revision"`
	Subject        metering.SubjectRef          `json:"subject"`
	Scope          string                       `json:"scope,omitempty"`
	Payer          metering.PaymentParty        `json:"payer,omitzero"`
	CoverageRefs   []metering.ChargeCoverageRef `json:"coverage_refs,omitempty"`
	Policy         VersionRef                   `json:"policy"`

	InputSetHash      string `json:"input_set_hash,omitempty"`
	LocalInputHash    string `json:"local_input_hash,omitempty"`
	ProviderInputHash string `json:"provider_input_hash,omitempty"`

	ValuationIDs    []string                  `json:"valuation_ids,omitempty"`
	ObservationRefs []metering.ObservationRef `json:"observation_refs,omitempty"`

	Quantity  *ComponentQuantityComparison   `json:"quantity,omitempty"`
	Monetary  *MonetaryDiscrepancyComparison `json:"monetary,omitempty"`
	Aggregate *ReconciliationAggregate       `json:"aggregate,omitempty"`

	Diagnostics []ReconciliationRetentionDiagnostic `json:"diagnostics,omitempty"`
	CreatedAt   time.Time                           `json:"created_at"`
}

// Validate checks the durable identity, scope, policy binding, evidence
// references and payload bound without mutating the value.
func (r ReconciliationRetentionResult) Validate() error {
	if r.SchemaVersion != ReconciliationRetentionSchemaVersionV1 {
		return fmt.Errorf("%w: unsupported schema version %d", ErrInvalidReconciliationRetention, r.SchemaVersion)
	}
	if !validEconomicIdentity(r.ID, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: id is required", ErrInvalidReconciliationRetention)
	}
	if r.ResultRevision == 0 {
		return fmt.Errorf("%w: result revision is required", ErrInvalidReconciliationRetention)
	}
	if err := r.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: subject: %v", ErrInvalidReconciliationRetention, err)
	}
	if r.Scope != "" && !validEconomicIdentity(r.Scope, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: scope is not a bounded identity", ErrInvalidReconciliationRetention)
	}
	if err := r.Payer.Validate(); err != nil {
		return fmt.Errorf("%w: payer: %v", ErrInvalidReconciliationRetention, err)
	}
	if !validEconomicIdentity(r.Policy.ID, metering.MaxSchemaIDBytes) || !validEconomicIdentity(r.Policy.Version, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: policy id and version are required", ErrInvalidReconciliationRetention)
	}
	for name, hash := range map[string]string{
		"input_set_hash": r.InputSetHash, "local_input_hash": r.LocalInputHash, "provider_input_hash": r.ProviderInputHash,
	} {
		if hash == "" {
			continue
		}
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != 32 {
			return fmt.Errorf("%w: %s must be SHA-256 hex", ErrInvalidReconciliationRetention, name)
		}
	}
	if len(r.ValuationIDs) > MaxReconciliationRetentionValuations {
		return fmt.Errorf("%w: valuation ids exceed %d", ErrInvalidReconciliationRetention, MaxReconciliationRetentionValuations)
	}
	for i, id := range r.ValuationIDs {
		if !validEconomicIdentity(id, metering.MaxSchemaIDBytes) {
			return fmt.Errorf("%w: valuation id %d is not a bounded identity", ErrInvalidReconciliationRetention, i)
		}
	}
	if len(r.Diagnostics) > MaxReconciliationRetentionDiagnostics {
		return fmt.Errorf("%w: diagnostics exceed %d", ErrInvalidReconciliationRetention, MaxReconciliationRetentionDiagnostics)
	}
	for i, diagnostic := range r.Diagnostics {
		if !validEconomicIdentity(diagnostic.Code, metering.MaxSchemaIDBytes) {
			return fmt.Errorf("%w: diagnostic %d code is required", ErrInvalidReconciliationRetention, i)
		}
		if !reconciliationFindingReasonKnown(diagnostic.Reason) {
			return fmt.Errorf("%w: diagnostic %d reason %q is not in the retained finding/diagnostic reason vocabulary", ErrInvalidReconciliationRetention, i, diagnostic.Reason)
		}
		if !validEconomicEvidenceText(diagnostic.Detail, MaxReconciliationRetentionDiagnosticBytes) {
			return fmt.Errorf("%w: diagnostic %d detail is not safe bounded text", ErrInvalidReconciliationRetention, i)
		}
	}
	if r.Quantity == nil && r.Monetary == nil && r.Aggregate == nil {
		return fmt.Errorf("%w: at least one comparison result is required", ErrInvalidReconciliationRetention)
	}
	if r.Aggregate != nil && (r.Aggregate.Policy.ID != r.Policy.ID || r.Aggregate.Policy.Version != r.Policy.Version) {
		return fmt.Errorf("%w: aggregate policy does not match retention policy", ErrInvalidReconciliationRetention)
	}
	if err := validateRetentionNested(r); err != nil {
		return err
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("%w: created_at is required", ErrInvalidReconciliationRetention)
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("%w: encode: %v", ErrInvalidReconciliationRetention, err)
	}
	if len(encoded) > MaxReconciliationRetentionBytes {
		return fmt.Errorf("%w: payload exceeds %d bytes", ErrInvalidReconciliationRetention, MaxReconciliationRetentionBytes)
	}
	return nil
}

// Normalize validates, deep-copies and deterministically orders the result so
// the same evidence produces one durable identity regardless of input order.
func (r ReconciliationRetentionResult) Normalize() (ReconciliationRetentionResult, error) {
	if err := r.Validate(); err != nil {
		return ReconciliationRetentionResult{}, err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return ReconciliationRetentionResult{}, fmt.Errorf("%w: encode: %v", ErrInvalidReconciliationRetention, err)
	}
	var out ReconciliationRetentionResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil {
		return ReconciliationRetentionResult{}, fmt.Errorf("%w: decode: %v", ErrInvalidReconciliationRetention, err)
	}
	out.canonicalize()
	if err := out.Validate(); err != nil {
		return ReconciliationRetentionResult{}, err
	}
	return out, nil
}

// CanonicalJSON returns the deterministic durable payload with sorted object
// keys, exactly matching the storage canonicalization contract.
func (r ReconciliationRetentionResult) CanonicalJSON() ([]byte, error) {
	normalized, err := r.Normalize()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("%w: encode: %v", ErrInvalidReconciliationRetention, err)
	}
	canonical, err := canonicalRetentionJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize: %v", ErrInvalidReconciliationRetention, err)
	}
	return canonical, nil
}

// Fingerprint returns the SHA-256 hex digest of the canonical payload. It is
// an accelerator; stored bytes remain the authoritative identity.
func (r ReconciliationRetentionResult) Fingerprint() string {
	payload, err := r.CanonicalJSON()
	if err != nil {
		return ""
	}
	return retentionFingerprint(payload)
}

// ParseReconciliationRetentionResult decodes a stored payload and verifies it
// is exactly canonical before returning it.
func ParseReconciliationRetentionResult(payload []byte) (ReconciliationRetentionResult, error) {
	if len(payload) == 0 {
		return ReconciliationRetentionResult{}, fmt.Errorf("%w: empty payload", ErrInvalidReconciliationRetention)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var result ReconciliationRetentionResult
	if err := decoder.Decode(&result); err != nil {
		return ReconciliationRetentionResult{}, fmt.Errorf("%w: decode: %v", ErrInvalidReconciliationRetention, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ReconciliationRetentionResult{}, fmt.Errorf("%w: multiple JSON values", ErrInvalidReconciliationRetention)
		}
		return ReconciliationRetentionResult{}, fmt.Errorf("%w: trailing data: %v", ErrInvalidReconciliationRetention, err)
	}
	normalized, err := result.Normalize()
	if err != nil {
		return ReconciliationRetentionResult{}, err
	}
	canonical, err := normalized.CanonicalJSON()
	if err != nil {
		return ReconciliationRetentionResult{}, err
	}
	if !bytes.Equal(canonical, payload) {
		return ReconciliationRetentionResult{}, fmt.Errorf("%w: payload is not the canonical result", ErrInvalidReconciliationRetention)
	}
	return normalized, nil
}

func (r *ReconciliationRetentionResult) canonicalize() {
	r.ValuationIDs = canonicalReconciliationStrings(r.ValuationIDs)
	r.ObservationRefs = canonicalReconciliationObservationRefs(r.ObservationRefs)
	if len(r.CoverageRefs) > 0 {
		coverage := append([]metering.ChargeCoverageRef(nil), r.CoverageRefs...)
		sort.SliceStable(coverage, func(i, j int) bool {
			return retentionCoverageSortKey(coverage[i]) < retentionCoverageSortKey(coverage[j])
		})
		r.CoverageRefs = coverage
	}
	if r.Quantity != nil {
		canonicalizeRetentionQuantity(r.Quantity)
	}
	if r.Monetary != nil {
		if r.Monetary.Quantity != nil {
			canonicalizeRetentionQuantity(r.Monetary.Quantity)
		}
		if len(r.Monetary.Rows) > 1 {
			rows := append([]MonetaryDiscrepancyRow(nil), r.Monetary.Rows...)
			sort.SliceStable(rows, func(i, j int) bool { return rows[i].Currency < rows[j].Currency })
			r.Monetary.Rows = rows
		}
		if len(r.Monetary.Valuations) > 1 {
			valuations := append([]MonetaryValuationEvidence(nil), r.Monetary.Valuations...)
			sort.SliceStable(valuations, func(i, j int) bool {
				left, right := retentionRoleRank(valuations[i].Role), retentionRoleRank(valuations[j].Role)
				if left != right {
					return left < right
				}
				return valuations[i].Role < valuations[j].Role
			})
			r.Monetary.Valuations = valuations
		}
	}
	if r.Aggregate != nil {
		if len(r.Aggregate.Findings) > 0 {
			findings := make([]ReconciliationFinding, len(r.Aggregate.Findings))
			for i, finding := range r.Aggregate.Findings {
				findings[i] = cloneReconciliationFinding(finding)
			}
			sort.Slice(findings, func(i, j int) bool {
				return retentionFindingSortKey(findings[i]) < retentionFindingSortKey(findings[j])
			})
			r.Aggregate.Findings = findings
		}
		if len(r.Aggregate.Rows) > 1 {
			rows := append([]ReconciliationAggregateRow(nil), r.Aggregate.Rows...)
			sort.SliceStable(rows, func(i, j int) bool {
				if rows[i].Scope != rows[j].Scope {
					return rows[i].Scope < rows[j].Scope
				}
				if rows[i].Currency != rows[j].Currency {
					return rows[i].Currency < rows[j].Currency
				}
				return rows[i].Unit < rows[j].Unit
			})
			r.Aggregate.Rows = rows
		}
		for i := range r.Aggregate.Rows {
			canonicalizeReconciliationAggregateRow(&r.Aggregate.Rows[i])
		}
	}
	if len(r.Diagnostics) > 1 {
		diagnostics := append([]ReconciliationRetentionDiagnostic(nil), r.Diagnostics...)
		sort.SliceStable(diagnostics, func(i, j int) bool {
			if diagnostics[i].Code != diagnostics[j].Code {
				return diagnostics[i].Code < diagnostics[j].Code
			}
			if diagnostics[i].Reason != diagnostics[j].Reason {
				return diagnostics[i].Reason < diagnostics[j].Reason
			}
			return diagnostics[i].Detail < diagnostics[j].Detail
		})
		r.Diagnostics = diagnostics
	}
}

func canonicalizeReconciliationAggregateRow(row *ReconciliationAggregateRow) {
	row.MissingIDs = canonicalReconciliationStrings(row.MissingIDs)
	row.IncomparableIDs = canonicalReconciliationStrings(row.IncomparableIDs)
	row.ConflictIDs = canonicalReconciliationStrings(row.ConflictIDs)
	if len(row.StatusCounts) > 1 {
		counts := append([]ReconciliationStatusCount(nil), row.StatusCounts...)
		sort.SliceStable(counts, func(i, j int) bool {
			return reconciliationStatusRank(counts[i].Status) < reconciliationStatusRank(counts[j].Status)
		})
		row.StatusCounts = counts
	}
}

func reconciliationStatusRank(status ReconciliationComparisonStatus) int {
	for i, candidate := range reconciliationStatusCountOrder {
		if candidate == status {
			return i
		}
	}
	return len(reconciliationStatusCountOrder)
}

func canonicalizeRetentionQuantity(in *ComponentQuantityComparison) {
	if in == nil {
		return
	}
	if len(in.Items) > 1 {
		items := append([]ComponentQuantityComparisonItem(nil), in.Items...)
		sort.Slice(items, func(i, j int) bool {
			return retentionQuantityItemSortKey(items[i]) < retentionQuantityItemSortKey(items[j])
		})
		in.Items = items
	}
	for i := range in.Items {
		in.Items[i].Local = canonicalizeRetentionEvidence(in.Items[i].Local)
		in.Items[i].Provider = canonicalizeRetentionEvidence(in.Items[i].Provider)
	}
}

func canonicalizeRetentionEvidence(in []ReconciliationQuantityEvidence) []ReconciliationQuantityEvidence {
	if len(in) <= 1 {
		return in
	}
	out := append([]ReconciliationQuantityEvidence(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		return retentionEvidenceSortKey(out[i]) < retentionEvidenceSortKey(out[j])
	})
	return out
}

// retentionQuantityItemSortKey is a total semantic key: duplicate item
// identities are rejected during validation, and the status/reason suffix keeps
// the order total even for otherwise identical keys.
func retentionQuantityItemSortKey(item ComponentQuantityComparisonItem) string {
	return item.Key.CanonicalKey() + "\x00" + string(item.Status) + "\x00" + string(item.Reason)
}

func retentionEvidenceSortKey(evidence ReconciliationQuantityEvidence) string {
	observation := evidence.Observation
	value := "nil"
	if evidence.Value != nil {
		value = evidence.Value.CanonicalString()
	}
	return observation.StoreID + "\x00" + observation.ObservationID + "\x00" +
		fmt.Sprint(observation.Revision) + "\x00" + observation.PayloadHash + "\x00" +
		evidence.Key.CanonicalKey() + "\x00" + evidence.Quality + "\x00" + evidence.MethodRef + "\x00" + value
}

// retentionFindingSortKey is the total semantic key for aggregate findings;
// duplicate finding identities are rejected during validation.
func retentionFindingSortKey(finding ReconciliationFinding) string {
	return finding.Scope + "\x00" + finding.Currency + "\x00" + finding.Unit + "\x00" + finding.ID + "\x00" +
		string(finding.Status) + "\x00" + string(finding.EvaluatedStatus) + "\x00" +
		string(finding.Reason) + "\x00" + string(finding.EvaluationReason)
}

func retentionCoverageSortKey(edge metering.ChargeCoverageRef) string {
	return edge.Ref.StoreID + "\x00" + edge.Ref.ObservationID + "\x00" +
		fmt.Sprint(edge.Ref.Revision) + "\x00" + edge.Ref.ChargeItemID + "\x00" + string(edge.Relation)
}

func retentionRoleRank(role MonetaryDiscrepancyRole) int {
	switch role {
	case MonetaryRoleE:
		return 0
	case MonetaryRoleQ:
		return 1
	case MonetaryRoleP:
		return 2
	default:
		return 3
	}
}

func retentionFingerprint(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func canonicalRetentionJSON(raw []byte) ([]byte, error) {
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
