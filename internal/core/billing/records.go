// Package billing contains provider-neutral billing domain values and policies.
//
// The package intentionally does not import lipapi, provider SDKs, database
// packages, or runtime orchestration. Adapters map wire evidence into these
// plain values at the boundary; sealed records contain only billing-safe
// identifiers, quantities, outcomes, and snapshot references.
package billing

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

const CurrentRecordSchemaVersion = 1

var (
	ErrInvalidRecord     = errors.New("billing: invalid usage record")
	ErrReplayConflict    = errors.New("billing: replay fingerprint conflict")
	ErrReplayKeyMismatch = errors.New("billing: durable key mismatch")
)

// VersionRef identifies an immutable pricing, policy, or operator-rate snapshot.
type VersionRef struct {
	ID          string
	Version     string
	EffectiveAt time.Time
	FetchedAt   time.Time
}

// Quantity preserves omitted versus explicitly reported zero quantities.
type Quantity struct {
	Value   int64
	Present bool
}

// MoneyEvidence preserves omitted versus explicitly reported provider cost.
type MoneyEvidence struct {
	NanoUnits int64
	Currency  string
	Present   bool
}

// EvidenceSource identifies the source of final B-leg evidence.
type EvidenceSource string

const (
	EvidenceSourceUnknown          EvidenceSource = ""
	EvidenceSourceProviderReported EvidenceSource = "provider_reported"
	EvidenceSourceProviderCountAPI EvidenceSource = "provider_count_api"
	EvidenceSourceLocalTokenizer   EvidenceSource = "local_tokenizer"
	EvidenceSourceLocalEstimator   EvidenceSource = "local_estimator"
	EvidenceSourceUnavailable      EvidenceSource = "unavailable"
)

// EvidenceAuthority describes how strongly final evidence may be relied upon.
type EvidenceAuthority string

const (
	EvidenceAuthorityUnknown       EvidenceAuthority = ""
	EvidenceAuthorityAuthoritative EvidenceAuthority = "authoritative"
	EvidenceAuthorityDelegated     EvidenceAuthority = "delegated"
	EvidenceAuthorityEstimated     EvidenceAuthority = "estimated"
	EvidenceAuthorityUnavailable   EvidenceAuthority = "unavailable"
)

// FinalBillingEvidence is the normalized, provider-neutral evidence sealed on a
// LUR. Presence is independent for each quantity and for monetary cost, so an
// explicit zero is never confused with an omitted field.
type FinalBillingEvidence struct {
	InputTokens      Quantity
	OutputTokens     Quantity
	CacheReadTokens  Quantity
	CacheWriteTokens Quantity
	ReasoningTokens  Quantity
	TotalTokens      Quantity
	Cost             MoneyEvidence
	Source           EvidenceSource
	Authority        EvidenceAuthority
	DedupeKey        string
}

// TurnOutcome is the terminal outcome of one billable A-leg/logical turn.
type TurnOutcome string

const (
	TurnOutcomeCompleted TurnOutcome = "completed"
	TurnOutcomeFailed    TurnOutcome = "failed"
	TurnOutcomeCanceled  TurnOutcome = "canceled"
	TurnOutcomeUnknown   TurnOutcome = "unknown"
)

// LegOutcome is the terminal role of one B-leg.
type LegOutcome string

const (
	LegOutcomeWinner    LegOutcome = "winner"
	LegOutcomeLoser     LegOutcome = "loser"
	LegOutcomeFailed    LegOutcome = "failed"
	LegOutcomeCanceled  LegOutcome = "canceled"
	LegOutcomeSwallowed LegOutcome = "swallowed_failure"
	LegOutcomeUnknown   LegOutcome = "unknown"
)

// SurfacedState records whether output from a B-leg reached the client.
type SurfacedState string

const (
	SurfacedYes     SurfacedState = "yes"
	SurfacedNo      SurfacedState = "no"
	SurfacedUnknown SurfacedState = "unknown"
)

// LegUsageRecord is immutable final evidence for one provider B-leg. Every
// valid LUR is provider-billable by construction: ProviderID identifies the
// provider attempt, and missing cost remains an explicit unreconciled outcome
// rather than an implicit non-billable leg. Key and Fingerprint are assigned by
// TurnUsageRecord.Seal and are not inputs to semantic hashing.
type LegUsageRecord struct {
	Key         string
	Fingerprint string

	ALegID string
	BLegID string
	Seq    int

	BackendID  string
	ProviderID string
	ModelID    string

	StartedAt  time.Time
	FinishedAt time.Time
	Outcome    LegOutcome
	Surfaced   SurfacedState

	Evidence        FinalBillingEvidence
	OperatorRateRef VersionRef
}

// TurnUsageRecord is one immutable billable A-leg/logical turn and its ordered
// B-leg evidence. Processing state, worker leases, and settlement results do
// not belong in this value.
type TurnUsageRecord struct {
	SchemaVersion int
	Key           string
	Fingerprint   string

	AccountID       string
	TurnID          string
	ALegID          string
	AuthorizationID string
	// SessionID is the proxy-owned AuthoritativeSessionID. Empty is allowed and
	// means the turn is not a member of any session report. Client session
	// hints never belong here.
	SessionID string

	StartedAt  time.Time
	FinishedAt time.Time
	Outcome    TurnOutcome

	CustomerPricingRef VersionRef
	ChargePolicyRef    VersionRef

	Legs []LegUsageRecord
}

// TURKey returns the durable A-leg identity required by billing records.
// Components must not contain ':' — naive concatenation would otherwise make
// account "a" + turn "b:c" collide with account "a:b" + turn "c" under a
// global tur_key primary key (hold keys already length-prefix for this reason).
func TURKey(accountID, turnID string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	turnID = strings.TrimSpace(turnID)
	if accountID == "" || turnID == "" {
		return "", fmt.Errorf("%w: account and turn identity are required", ErrInvalidRecord)
	}
	if strings.Contains(accountID, ":") || strings.Contains(turnID, ":") {
		return "", fmt.Errorf("%w: account and turn identity must not contain ':'", ErrInvalidRecord)
	}
	return accountID + ":" + turnID, nil
}

// LURKey returns the durable B-leg identity under a TUR.
func LURKey(turKey, bLegID string) (string, error) {
	turKey = strings.TrimSpace(turKey)
	bLegID = strings.TrimSpace(bLegID)
	if turKey == "" || bLegID == "" {
		return "", fmt.Errorf("%w: TUR and B-leg identity are required", ErrInvalidRecord)
	}
	if strings.Contains(bLegID, ":") {
		return "", fmt.Errorf("%w: B-leg identity must not contain ':'", ErrInvalidRecord)
	}
	return turKey + ":" + bLegID, nil
}

// Seal validates an evidence record and returns a detached copy with derived
// durable keys and semantic fingerprints. The receiver and its Legs slice are
// never mutated, which keeps the input suitable for caller-side reuse.
func (r TurnUsageRecord) Seal() (TurnUsageRecord, error) {
	if err := r.validate(); err != nil {
		return TurnUsageRecord{}, err
	}
	out := r
	key, err := TURKey(r.AccountID, r.TurnID)
	if err != nil {
		return TurnUsageRecord{}, err
	}
	out.Key = key
	out.Legs = append([]LegUsageRecord(nil), r.Legs...)
	for i := range out.Legs {
		lurKey, err := LURKey(out.Key, out.Legs[i].BLegID)
		if err != nil {
			return TurnUsageRecord{}, err
		}
		out.Legs[i].Key = lurKey
		fp, err := out.Legs[i].semanticFingerprint(out.Key)
		if err != nil {
			return TurnUsageRecord{}, err
		}
		out.Legs[i].Fingerprint = fp
	}
	fp, err := out.SemanticFingerprint()
	if err != nil {
		return TurnUsageRecord{}, err
	}
	out.Fingerprint = fp
	return out, nil
}

// SemanticFingerprint computes the versioned canonical fingerprint while
// ignoring stored Key/Fingerprint fields and all database/processing metadata.
func (r TurnUsageRecord) SemanticFingerprint() (string, error) {
	if err := r.validate(); err != nil {
		return "", err
	}
	var c canonicalWriter
	parentKey, err := TURKey(r.AccountID, r.TurnID)
	if err != nil {
		return "", err
	}
	c.string("tur")
	c.u64(uint64(r.SchemaVersion))
	c.string(r.AccountID)
	c.string(r.TurnID)
	c.string(r.ALegID)
	c.string(r.AuthorizationID)
	c.string(r.SessionID)
	c.time(r.StartedAt)
	c.time(r.FinishedAt)
	c.string(string(r.Outcome))
	writeVersionRef(&c, r.CustomerPricingRef)
	writeVersionRef(&c, r.ChargePolicyRef)
	c.u64(uint64(len(r.Legs)))
	for _, leg := range r.Legs {
		if err := writeLegCanonical(&c, parentKey, leg); err != nil {
			return "", err
		}
	}
	return digest(c.bytes()), nil
}

func (l LegUsageRecord) semanticFingerprint(parentKey string) (string, error) {
	var c canonicalWriter
	c.string("lur")
	c.string(parentKey)
	if err := writeLegCanonical(&c, parentKey, l); err != nil {
		return "", err
	}
	return digest(c.bytes()), nil
}

// CheckReplay accepts an exact replay and rejects a conflicting durable record.
// A caller/store must compare before any mutation.
func CheckReplay(existing, incoming TurnUsageRecord) error {
	if err := validateSealed(existing); err != nil {
		return err
	}
	if err := validateSealed(incoming); err != nil {
		return err
	}
	if existing.Key != incoming.Key {
		return ErrReplayKeyMismatch
	}
	if existing.Fingerprint != incoming.Fingerprint {
		return ErrReplayConflict
	}
	return nil
}

func validateSealed(r TurnUsageRecord) error {
	if strings.TrimSpace(r.Key) == "" || strings.TrimSpace(r.Fingerprint) == "" {
		return fmt.Errorf("%w: replay requires sealed key and fingerprint", ErrInvalidRecord)
	}
	key, err := TURKey(r.AccountID, r.TurnID)
	if err != nil || key != r.Key {
		return fmt.Errorf("%w: TUR key does not match immutable identity", ErrInvalidRecord)
	}
	fp, err := r.SemanticFingerprint()
	if err != nil {
		return err
	}
	if fp != r.Fingerprint {
		return fmt.Errorf("%w: TUR fingerprint does not match immutable content", ErrInvalidRecord)
	}
	for _, leg := range r.Legs {
		lurKey, err := LURKey(r.Key, leg.BLegID)
		if err != nil || lurKey != leg.Key {
			return fmt.Errorf("%w: LUR key does not match immutable identity", ErrInvalidRecord)
		}
		lurFP, err := leg.semanticFingerprint(r.Key)
		if err != nil {
			return err
		}
		if lurFP != leg.Fingerprint {
			return fmt.Errorf("%w: LUR fingerprint does not match immutable content", ErrInvalidRecord)
		}
	}
	return nil
}

func (r TurnUsageRecord) validate() error {
	if r.SchemaVersion != CurrentRecordSchemaVersion {
		return fmt.Errorf("%w: unsupported schema version %d", ErrInvalidRecord, r.SchemaVersion)
	}
	for name, value := range map[string]string{
		"account": r.AccountID, "turn": r.TurnID, "A-leg": r.ALegID, "authorization": r.AuthorizationID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: %s identity is required", ErrInvalidRecord, name)
		}
	}
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) {
		return fmt.Errorf("%w: invalid TUR timestamps", ErrInvalidRecord)
	}
	if !validTurnOutcome(r.Outcome) {
		return fmt.Errorf("%w: unknown TUR outcome %q", ErrInvalidRecord, r.Outcome)
	}
	if len(r.Legs) == 0 {
		return fmt.Errorf("%w: at least one LUR is required", ErrInvalidRecord)
	}
	lastSeq := 0
	seen := make(map[string]struct{}, len(r.Legs))
	for _, leg := range r.Legs {
		if err := leg.validate(r.ALegID); err != nil {
			return err
		}
		if leg.Seq <= lastSeq {
			return fmt.Errorf("%w: LUR sequence must be strictly increasing", ErrInvalidRecord)
		}
		lastSeq = leg.Seq
		if _, ok := seen[leg.BLegID]; ok {
			return fmt.Errorf("%w: duplicate B-leg %q", ErrInvalidRecord, leg.BLegID)
		}
		seen[leg.BLegID] = struct{}{}
	}
	return nil
}

func (l LegUsageRecord) validate(aLegID string) error {
	if strings.TrimSpace(l.ALegID) == "" || l.ALegID != aLegID || strings.TrimSpace(l.BLegID) == "" {
		return fmt.Errorf("%w: LUR lineage does not match TUR", ErrInvalidRecord)
	}
	if l.Seq <= 0 || strings.TrimSpace(l.BackendID) == "" || strings.TrimSpace(l.ProviderID) == "" || strings.TrimSpace(l.ModelID) == "" {
		return fmt.Errorf("%w: LUR identity/provider/model fields are required", ErrInvalidRecord)
	}
	if l.StartedAt.IsZero() || l.FinishedAt.IsZero() || l.FinishedAt.Before(l.StartedAt) {
		return fmt.Errorf("%w: invalid LUR timestamps", ErrInvalidRecord)
	}
	if !validLegOutcome(l.Outcome) || !validSurfacedState(l.Surfaced) {
		return fmt.Errorf("%w: invalid LUR outcome/surfaced state", ErrInvalidRecord)
	}
	if err := validateEvidence(l.Evidence); err != nil {
		return err
	}
	return nil
}

func validateEvidence(e FinalBillingEvidence) error {
	for name, q := range map[string]Quantity{
		"input": e.InputTokens, "output": e.OutputTokens, "cache_read": e.CacheReadTokens,
		"cache_write": e.CacheWriteTokens, "reasoning": e.ReasoningTokens, "total": e.TotalTokens,
	} {
		if q.Value < 0 || (!q.Present && q.Value != 0) {
			return fmt.Errorf("%w: invalid %s quantity presence/value", ErrInvalidRecord, name)
		}
	}
	if e.Cost.NanoUnits < 0 || (!e.Cost.Present && e.Cost.NanoUnits != 0) {
		return fmt.Errorf("%w: invalid cost presence/value", ErrInvalidRecord)
	}
	if e.Cost.Present && strings.TrimSpace(e.Cost.Currency) == "" {
		return fmt.Errorf("%w: present cost requires currency", ErrInvalidRecord)
	}
	return nil
}

func validTurnOutcome(v TurnOutcome) bool {
	return v == TurnOutcomeCompleted || v == TurnOutcomeFailed || v == TurnOutcomeCanceled || v == TurnOutcomeUnknown
}

func validLegOutcome(v LegOutcome) bool {
	return v == LegOutcomeWinner || v == LegOutcomeLoser || v == LegOutcomeFailed || v == LegOutcomeCanceled || v == LegOutcomeSwallowed || v == LegOutcomeUnknown
}

func validSurfacedState(v SurfacedState) bool {
	return v == SurfacedYes || v == SurfacedNo || v == SurfacedUnknown
}

func writeLegCanonical(c *canonicalWriter, parentKey string, l LegUsageRecord) error {
	c.string(parentKey)
	c.string(l.ALegID)
	c.string(l.BLegID)
	c.u64(uint64(l.Seq))
	c.string(l.BackendID)
	c.string(l.ProviderID)
	c.string(l.ModelID)
	c.time(l.StartedAt)
	c.time(l.FinishedAt)
	c.string(string(l.Outcome))
	c.string(string(l.Surfaced))
	writeQuantity(c, l.Evidence.InputTokens)
	writeQuantity(c, l.Evidence.OutputTokens)
	writeQuantity(c, l.Evidence.CacheReadTokens)
	writeQuantity(c, l.Evidence.CacheWriteTokens)
	writeQuantity(c, l.Evidence.ReasoningTokens)
	writeQuantity(c, l.Evidence.TotalTokens)
	c.i64(l.Evidence.Cost.NanoUnits)
	c.boolean(l.Evidence.Cost.Present)
	c.string(l.Evidence.Cost.Currency)
	c.string(string(l.Evidence.Source))
	c.string(string(l.Evidence.Authority))
	c.string(l.Evidence.DedupeKey)
	writeVersionRef(c, l.OperatorRateRef)
	return nil
}

func writeQuantity(c *canonicalWriter, q Quantity) {
	c.i64(q.Value)
	c.boolean(q.Present)
}

func writeVersionRef(c *canonicalWriter, r VersionRef) {
	c.string(r.ID)
	c.string(r.Version)
	c.time(r.EffectiveAt)
	c.time(r.FetchedAt)
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}

type canonicalWriter struct{ data []byte }

func (w *canonicalWriter) bytes() []byte { return w.data }
func (w *canonicalWriter) string(v string) {
	w.u64(uint64(len(v)))
	w.data = append(w.data, v...)
}

func (w *canonicalWriter) u64(v uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	w.data = append(w.data, buf[:]...)
}
func (w *canonicalWriter) i64(v int64) { w.u64(uint64(v)) }
func (w *canonicalWriter) boolean(v bool) {
	if v {
		w.data = append(w.data, 1)
		return
	}
	w.data = append(w.data, 0)
}

func (w *canonicalWriter) time(v time.Time) {
	if v.IsZero() {
		w.i64(0)
		return
	}
	w.i64(v.UTC().UnixNano())
}
