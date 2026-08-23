package reasoningpreservation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

// CompressionLimits configures bounded optional-state budgets. Zero means unlimited
// for backward compatibility with disabled compression.
type CompressionLimits struct {
	MaxPendingPerSession        int
	MaxPendingTotal             int
	MaxSurrogateBytesPerTurn    int
	MaxSurrogateBytesPerSession int
	MaxSurrogateBytesTotal      int
}

// SurrogateSegment is a minimal placement-indexed surrogate text.
type SurrogateSegment struct {
	PlacementIndex int
	Text           string
	Bytes          int
}

// ReasoningSurrogate is the validated optional replacement for semantic-text placements.
// SemanticDigest is the SHA-256 of the canonical semantic-text payload that was
// compressed; EgressPolicyHash is the hash of the egress policy version that
// authorized submission. Both are immutable correlation digests verified by
// AttachSurrogate CAS. A zero SemanticDigest is invalid (real artifacts always
// have content) and Attach rejects it as a conflict.
type ReasoningSurrogate struct {
	OriginalDigest   [32]byte
	PolicyRevision   string
	Sanitization     string
	Segments         []SurrogateSegment
	Bytes            int
	SemanticDigest   [32]byte
	EgressPolicyHash [32]byte
}

// PendingCompression tracks a reservation awaiting background result.
// SemanticDigest is the SHA-256 of the source semantic text; EgressPolicyHash
// identifies the egress policy revision that authorized the pending work.
// Both are recorded at Reserve and immutable via Bind; Attach verifies they
// match. Zero SemanticDigest is invalid and Attach rejects it as a
// conflict (real artifacts have content). Zero EgressPolicyHash is allowed
// but CAS-checked — a mismatch still yields a conflict.
// PolicyHashAuthoritative is false at Reserve (provisional ref hash) and
// becomes true after successful UpdateReservationPolicyHash CAS promotion.
// Bind and Attach reject unless authoritative.
type PendingCompression struct {
	JobID                   auxiliary.JobID
	OriginalDigest          [32]byte
	SemanticDigest          [32]byte
	EgressPolicyHash        [32]byte
	PolicyHashAuthoritative bool
	CreatedAt               time.Time
	PolicyRevision          string
	ReservationID           string
}

// CompressionState is additive optional state per artifact revision.
type CompressionState struct {
	PolicyRevision string
	ReservationID  string
	Pending        *PendingCompression
	Surrogate      *ReasoningSurrogate
}

// CompressionStats exposes aggregate counters for tests.
type CompressionStats struct {
	TotalPending             int
	TotalSurrogateBytes      int
	PendingPerSession        map[string]int
	SurrogateBytesPerSession map[string]int
}

// BudgetKind distinguishes which limit was exceeded.
type BudgetKind string

const (
	BudgetPendingPerSession   BudgetKind = "pending_per_session"
	BudgetPendingTotal        BudgetKind = "pending_total"
	BudgetSurrogatePerTurn    BudgetKind = "surrogate_per_turn"
	BudgetSurrogatePerSession BudgetKind = "surrogate_per_session"
	BudgetSurrogateTotal      BudgetKind = "surrogate_total"
)

// BudgetError is returned when a per-session or aggregate budget rejects admission.
type BudgetError struct {
	Kind    BudgetKind
	Limit   int
	Current int
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf("%s: budget %s exceeded limit %d current %d", ID, e.Kind, e.Limit, e.Current)
}

func (e *BudgetError) Unwrap() error { return ErrCompressionBudgetExceeded }

var (
	ErrCompressionNotFound       = errors.New(ID + ": compression not found")
	ErrCompressionConflict       = errors.New(ID + ": compression conflict")
	ErrCompressionBudgetExceeded = errors.New(ID + ": compression budget exceeded")
)

// IsBudgetError reports whether err is a budget rejection.
func IsBudgetError(err error) bool { return errors.Is(err, ErrCompressionBudgetExceeded) }

// IsConflictError reports whether err is a CAS/stale conflict.
func IsConflictError(err error) bool { return errors.Is(err, ErrCompressionConflict) }

// IsNotFoundError reports whether err is a not-found.
func IsNotFoundError(err error) bool { return errors.Is(err, ErrCompressionNotFound) }

// CompressionStore extends TurnStore with optional-state operations.
type CompressionStore interface {
	TurnStore
	ReserveCompression(ctx context.Context, partition SessionPartition, artifactID string, originalDigest [32]byte, policyRevision string, semanticDigest [32]byte, egressPolicyHash [32]byte) (string, error)
	UpdateReservationPolicyHash(ctx context.Context, partition SessionPartition, artifactID string, reservationID string, expectedOldHash [32]byte, originalDigest [32]byte, policyRevision string, semanticDigest [32]byte, newHash [32]byte) error
	BindCompressionJob(ctx context.Context, partition SessionPartition, artifactID string, reservationID string, jobID auxiliary.JobID, originalDigest [32]byte, policyRevision string) error
	AttachSurrogate(ctx context.Context, partition SessionPartition, artifactID string, reservationID string, jobID auxiliary.JobID, surrogate ReasoningSurrogate) error
	ClearCompression(ctx context.Context, partition SessionPartition, artifactID string, expectedReservationID string) error
	GetCompressionState(ctx context.Context, partition SessionPartition, artifactID string) (CompressionState, bool, error)
	CompressionStats() CompressionStats
}

var _ CompressionStore = (*memoryTurnStore)(nil)

type compressionEntry struct {
	pending        *PendingCompression
	reservationID  string
	policyRevision string
	surrogate      *ReasoningSurrogate
	originalDigest [32]byte
}

func (s *memoryTurnStore) ReserveCompression(ctx context.Context, partition SessionPartition, artifactID string, originalDigest [32]byte, policyRevision string, semanticDigest [32]byte, egressPolicyHash [32]byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if artifactID == "" || policyRevision == "" {
		return "", fmt.Errorf("%w: missing id or policy", ErrCompressionConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := partition.key()
	now := s.opts.Now()
	// expire expired originals first to free counters
	_ = s.expireLocked(key, now, EvictionSummary{})
	list := s.by[key]
	found := false
	for i := range list {
		if list[i].ID == artifactID {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("%w: artifact %q not found", ErrCompressionNotFound, artifactID)
	}
	// check existing optional state
	if m, ok := s.compBy[key]; ok {
		if e, ok := m[artifactID]; ok {
			if e.pending != nil {
				return "", fmt.Errorf("%w: pending already exists for %q", ErrCompressionConflict, artifactID)
			}
			if e.surrogate != nil && e.surrogate.PolicyRevision == policyRevision {
				return "", fmt.Errorf("%w: surrogate already exists for %q policy %q", ErrCompressionConflict, artifactID, policyRevision)
			}
		}
	}
	// per-session pending
	if s.opts.CompressionLimits.MaxPendingPerSession > 0 && s.pendingPerSession[key] >= s.opts.CompressionLimits.MaxPendingPerSession {
		return "", &BudgetError{Kind: BudgetPendingPerSession, Limit: s.opts.CompressionLimits.MaxPendingPerSession, Current: s.pendingPerSession[key]}
	}
	if s.opts.CompressionLimits.MaxPendingTotal > 0 && s.totalPending >= s.opts.CompressionLimits.MaxPendingTotal {
		return "", &BudgetError{Kind: BudgetPendingTotal, Limit: s.opts.CompressionLimits.MaxPendingTotal, Current: s.totalPending}
	}
	s.reservationSeq++
	reservationID := fmt.Sprintf("res-%d", s.reservationSeq)
	if s.compBy[key] == nil {
		if s.compBy == nil {
			s.compBy = make(map[string]map[string]*compressionEntry)
		}
		s.compBy[key] = make(map[string]*compressionEntry)
	}
	entry, ok := s.compBy[key][artifactID]
	if !ok {
		entry = &compressionEntry{}
		s.compBy[key][artifactID] = entry
	}
	entry.originalDigest = originalDigest
	entry.policyRevision = policyRevision
	entry.reservationID = reservationID
	entry.pending = &PendingCompression{
		OriginalDigest:          originalDigest,
		SemanticDigest:          semanticDigest,
		EgressPolicyHash:        egressPolicyHash,
		PolicyHashAuthoritative: false,
		CreatedAt:               now,
		PolicyRevision:          policyRevision,
		ReservationID:           reservationID,
	}
	if s.pendingPerSession == nil {
		s.pendingPerSession = make(map[string]int)
	}
	s.pendingPerSession[key]++
	s.totalPending++
	return reservationID, nil
}

func (s *memoryTurnStore) UpdateReservationPolicyHash(ctx context.Context, partition SessionPartition, artifactID string, reservationID string, expectedOldHash [32]byte, originalDigest [32]byte, policyRevision string, semanticDigest [32]byte, newHash [32]byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if artifactID == "" || reservationID == "" || policyRevision == "" {
		return fmt.Errorf("%w: missing update fields", ErrCompressionConflict)
	}
	var zeroHash [32]byte
	if newHash == zeroHash {
		return fmt.Errorf("%w: zero authoritative hash", ErrCompressionConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := partition.key()
	m, ok := s.compBy[key]
	if !ok {
		return fmt.Errorf("%w: no pending for %q", ErrCompressionConflict, artifactID)
	}
	entry, ok := m[artifactID]
	if !ok || entry.pending == nil {
		return fmt.Errorf("%w: no pending for %q", ErrCompressionConflict, artifactID)
	}
	if entry.reservationID != reservationID || entry.pending.ReservationID != reservationID {
		return fmt.Errorf("%w: reservation mismatch", ErrCompressionConflict)
	}
	if entry.pending.EgressPolicyHash != expectedOldHash {
		return fmt.Errorf("%w: expected old hash mismatch", ErrCompressionConflict)
	}
	if entry.pending.OriginalDigest != originalDigest {
		return fmt.Errorf("%w: digest mismatch", ErrCompressionConflict)
	}
	if entry.pending.PolicyRevision != policyRevision {
		return fmt.Errorf("%w: policy mismatch", ErrCompressionConflict)
	}
	if entry.pending.SemanticDigest != semanticDigest {
		return fmt.Errorf("%w: semantic digest mismatch", ErrCompressionConflict)
	}
	if entry.pending.PolicyHashAuthoritative {
		return fmt.Errorf("%w: already authoritative", ErrCompressionConflict)
	}
	// verify artifact still exists
	list := s.by[key]
	found := false
	for i := range list {
		if list[i].ID == artifactID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: artifact not found", ErrCompressionNotFound)
	}
	entry.pending.EgressPolicyHash = newHash
	entry.pending.PolicyHashAuthoritative = true
	return nil
}

func (s *memoryTurnStore) BindCompressionJob(ctx context.Context, partition SessionPartition, artifactID string, reservationID string, jobID auxiliary.JobID, originalDigest [32]byte, policyRevision string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if reservationID == "" || jobID == "" || artifactID == "" {
		return fmt.Errorf("%w: missing binding fields", ErrCompressionConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := partition.key()
	m, ok := s.compBy[key]
	if !ok {
		return fmt.Errorf("%w: no pending for %q", ErrCompressionConflict, artifactID)
	}
	entry, ok := m[artifactID]
	if !ok || entry.pending == nil {
		return fmt.Errorf("%w: no pending for %q", ErrCompressionConflict, artifactID)
	}
	if entry.reservationID != reservationID {
		return fmt.Errorf("%w: reservation mismatch", ErrCompressionConflict)
	}
	if entry.pending.ReservationID != reservationID {
		return fmt.Errorf("%w: reservation mismatch", ErrCompressionConflict)
	}
	if entry.pending.OriginalDigest != originalDigest {
		return fmt.Errorf("%w: digest mismatch", ErrCompressionConflict)
	}
	if entry.pending.PolicyRevision != policyRevision {
		return fmt.Errorf("%w: policy mismatch", ErrCompressionConflict)
	}
	if !entry.pending.PolicyHashAuthoritative {
		return fmt.Errorf("%w: not authoritative", ErrCompressionConflict)
	}
	// verify artifact still exists
	list := s.by[key]
	found := false
	for i := range list {
		if list[i].ID == artifactID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: artifact not found", ErrCompressionNotFound)
	}
	entry.pending.JobID = jobID
	return nil
}

func (s *memoryTurnStore) AttachSurrogate(ctx context.Context, partition SessionPartition, artifactID string, reservationID string, jobID auxiliary.JobID, surrogate ReasoningSurrogate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if artifactID == "" || reservationID == "" {
		return fmt.Errorf("%w: missing attach fields", ErrCompressionConflict)
	}
	// Defensive copy outside lock to avoid holding mutex across allocation.
	clone := cloneSurrogate(surrogate)
	s.mu.Lock()
	defer s.mu.Unlock()
	key := partition.key()
	// verify artifact exists
	list := s.by[key]
	found := false
	for i := range list {
		if list[i].ID == artifactID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: artifact not found", ErrCompressionNotFound)
	}
	m, ok := s.compBy[key]
	if !ok {
		return fmt.Errorf("%w: no pending for %q", ErrCompressionConflict, artifactID)
	}
	entry, ok := m[artifactID]
	if !ok || entry.pending == nil {
		return fmt.Errorf("%w: no pending for %q", ErrCompressionConflict, artifactID)
	}
	if entry.reservationID != reservationID || entry.pending.ReservationID != reservationID {
		return fmt.Errorf("%w: reservation mismatch", ErrCompressionConflict)
	}
	if !entry.pending.PolicyHashAuthoritative {
		return fmt.Errorf("%w: not authoritative", ErrCompressionConflict)
	}
	if entry.pending.JobID != "" && entry.pending.JobID != jobID {
		return fmt.Errorf("%w: job mismatch", ErrCompressionConflict)
	}
	if entry.pending.OriginalDigest != clone.OriginalDigest {
		return fmt.Errorf("%w: digest mismatch", ErrCompressionConflict)
	}
	if entry.pending.PolicyRevision != clone.PolicyRevision {
		return fmt.Errorf("%w: policy mismatch", ErrCompressionConflict)
	}
	// Correlation-digest CAS: SemanticDigest and EgressPolicyHash must match
	// the pending reservation. Zero semantic digest is invalid (real artifact
	// always has content) and is rejected as a conflict.
	var zeroDigest [32]byte
	if clone.SemanticDigest == zeroDigest {
		return fmt.Errorf("%w: zero semantic digest", ErrCompressionConflict)
	}
	if entry.pending.SemanticDigest == zeroDigest {
		return fmt.Errorf("%w: zero semantic digest", ErrCompressionConflict)
	}
	if entry.pending.SemanticDigest != clone.SemanticDigest {
		return fmt.Errorf("%w: semantic digest mismatch", ErrCompressionConflict)
	}
	if entry.pending.EgressPolicyHash != clone.EgressPolicyHash {
		return fmt.Errorf("%w: egress policy hash mismatch", ErrCompressionConflict)
	}
	// oldBytes is the existing surrogate size for this artifact (replacement path).
	// By construction oldBytes <= cur session/total counters.
	oldBytes := 0
	if entry.surrogate != nil {
		oldBytes = entry.surrogate.Bytes
	}
	// budget checks with delta accounting for replacement.
	if s.opts.CompressionLimits.MaxSurrogateBytesPerTurn > 0 && clone.Bytes > s.opts.CompressionLimits.MaxSurrogateBytesPerTurn {
		return &BudgetError{Kind: BudgetSurrogatePerTurn, Limit: s.opts.CompressionLimits.MaxSurrogateBytesPerTurn, Current: clone.Bytes}
	}
	if s.opts.CompressionLimits.MaxSurrogateBytesPerSession > 0 {
		cur := s.surrogateBytesPerSession[key]
		// oldBytes <= cur by invariant; cur-oldBytes+new is the post-replacement total.
		if cur-oldBytes+clone.Bytes > s.opts.CompressionLimits.MaxSurrogateBytesPerSession {
			return &BudgetError{Kind: BudgetSurrogatePerSession, Limit: s.opts.CompressionLimits.MaxSurrogateBytesPerSession, Current: cur}
		}
	}
	if s.opts.CompressionLimits.MaxSurrogateBytesTotal > 0 {
		curTotal := s.totalSurrogateBytes
		if curTotal-oldBytes+clone.Bytes > s.opts.CompressionLimits.MaxSurrogateBytesTotal {
			return &BudgetError{Kind: BudgetSurrogateTotal, Limit: s.opts.CompressionLimits.MaxSurrogateBytesTotal, Current: curTotal}
		}
	}
	// success: decrement pending exactly once
	if s.pendingPerSession[key] > 0 {
		s.pendingPerSession[key]--
		if s.pendingPerSession[key] == 0 {
			delete(s.pendingPerSession, key)
		}
	}
	if s.totalPending > 0 {
		s.totalPending--
	}
	entry.surrogate = &clone
	entry.pending = nil
	// keep reservationID/policy for lookup but pending cleared
	if s.surrogateBytesPerSession == nil {
		s.surrogateBytesPerSession = make(map[string]int)
	}
	curSess := s.surrogateBytesPerSession[key]
	newSess := curSess - oldBytes + clone.Bytes
	if newSess <= 0 {
		delete(s.surrogateBytesPerSession, key)
	} else {
		s.surrogateBytesPerSession[key] = newSess
	}
	s.totalSurrogateBytes = s.totalSurrogateBytes - oldBytes + clone.Bytes
	return nil
}

func (s *memoryTurnStore) ClearCompression(ctx context.Context, partition SessionPartition, artifactID string, expectedReservationID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if artifactID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := partition.key()
	m, ok := s.compBy[key]
	if !ok {
		return nil
	}
	entry, ok := m[artifactID]
	if !ok {
		return nil
	}
	if expectedReservationID != "" && entry.reservationID != expectedReservationID {
		return fmt.Errorf("%w: reservation mismatch on clear", ErrCompressionConflict)
	}
	if expectedReservationID != "" && entry.pending != nil && entry.pending.ReservationID != expectedReservationID {
		return fmt.Errorf("%w: pending reservation mismatch on clear", ErrCompressionConflict)
	}
	s.clearOptionalLocked(key, artifactID)
	return nil
}

func (s *memoryTurnStore) GetCompressionState(ctx context.Context, partition SessionPartition, artifactID string) (CompressionState, bool, error) {
	if err := ctx.Err(); err != nil {
		return CompressionState{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := partition.key()
	m, ok := s.compBy[key]
	if !ok {
		return CompressionState{}, false, nil
	}
	entry, ok := m[artifactID]
	if !ok {
		return CompressionState{}, false, nil
	}
	state := CompressionState{
		PolicyRevision: entry.policyRevision,
		ReservationID:  entry.reservationID,
	}
	if entry.pending != nil {
		cp := *entry.pending
		state.Pending = &cp
	}
	if entry.surrogate != nil {
		cs := cloneSurrogate(*entry.surrogate)
		state.Surrogate = &cs
	}
	return state, true, nil
}

func (s *memoryTurnStore) CompressionStats() CompressionStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := CompressionStats{
		TotalPending:             s.totalPending,
		TotalSurrogateBytes:      s.totalSurrogateBytes,
		PendingPerSession:        make(map[string]int, len(s.pendingPerSession)),
		SurrogateBytesPerSession: make(map[string]int, len(s.surrogateBytesPerSession)),
	}
	for k, v := range s.pendingPerSession {
		out.PendingPerSession[k] = v
	}
	for k, v := range s.surrogateBytesPerSession {
		out.SurrogateBytesPerSession[k] = v
	}
	return out
}

func cloneSurrogate(in ReasoningSurrogate) ReasoningSurrogate {
	out := in
	if len(in.Segments) > 0 {
		out.Segments = make([]SurrogateSegment, len(in.Segments))
		copy(out.Segments, in.Segments)
	}
	return out
}
