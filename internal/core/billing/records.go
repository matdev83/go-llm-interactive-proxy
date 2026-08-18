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

type VersionRef struct {
	ID          string
	Version     string
	EffectiveAt time.Time
	FetchedAt   time.Time
}
type Quantity struct {
	Value   int64
	Present bool
}
type MoneyEvidence struct {
	NanoUnits int64
	Currency  string
	Present   bool
}
type EvidenceSource string

const (
	EvidenceSourceUnknown          EvidenceSource = ""
	EvidenceSourceProviderReported EvidenceSource = "provider_reported"
	EvidenceSourceProviderCountAPI EvidenceSource = "provider_count_api"
	EvidenceSourceLocalTokenizer   EvidenceSource = "local_tokenizer"
	EvidenceSourceLocalEstimator   EvidenceSource = "local_estimator"
	EvidenceSourceUnavailable      EvidenceSource = "unavailable"
)

type EvidenceAuthority string

const (
	EvidenceAuthorityUnknown       EvidenceAuthority = ""
	EvidenceAuthorityAuthoritative EvidenceAuthority = "authoritative"
	EvidenceAuthorityDelegated     EvidenceAuthority = "delegated"
	EvidenceAuthorityEstimated     EvidenceAuthority = "estimated"
	EvidenceAuthorityUnavailable   EvidenceAuthority = "unavailable"
)

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
type TurnOutcome string

const (
	TurnOutcomeCompleted TurnOutcome = "completed"
	TurnOutcomeFailed    TurnOutcome = "failed"
	TurnOutcomeCanceled  TurnOutcome = "canceled"
	TurnOutcomeUnknown   TurnOutcome = "unknown"
)

type LegOutcome string

const (
	LegOutcomeWinner       LegOutcome = "winner"
	LegOutcomeLoser        LegOutcome = "loser"
	LegOutcomeFailed       LegOutcome = "failed"
	LegOutcomeCanceled     LegOutcome = "canceled"
	LegOutcomeSwallowed    LegOutcome = "swallowed_failure"
	LegOutcomeRejected     LegOutcome = "rejected"
	LegOutcomeNeverStarted LegOutcome = "never_started"
	LegOutcomeUnknown      LegOutcome = "unknown"
)

type SurfacedState string

const (
	SurfacedYes     SurfacedState = "yes"
	SurfacedNo      SurfacedState = "no"
	SurfacedUnknown SurfacedState = "unknown"
)

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
	return v == LegOutcomeWinner || v == LegOutcomeLoser || v == LegOutcomeFailed || v == LegOutcomeCanceled || v == LegOutcomeSwallowed || v == LegOutcomeRejected || v == LegOutcomeNeverStarted || v == LegOutcomeUnknown
}

func validSurfacedState(v SurfacedState) bool {
	return v == SurfacedYes || v == SurfacedNo || v == SurfacedUnknown
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
