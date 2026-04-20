package b2bua

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Sentinel errors for A-leg continuity and validation.
var (
	ErrALegNotFound         = errors.New("b2bua: a-leg not found")
	ErrInvalidContinuityKey = errors.New("b2bua: continuity key required for resolve")
	ErrInvalidAttempt       = errors.New("b2bua: invalid attempt record")
)

// ALegRecord is the core-owned logical session row for routing and lineage.
type ALegRecord struct {
	ALegID                string
	ContinuityKey         string
	CreatedAt             time.Time
	LastSeenAt            time.Time
	WeightedFirstConsumed bool
}

// BLegRecord identifies one backend attempt slot within an A-leg.
type BLegRecord struct {
	BLegID string
	ALegID string
	Seq    int
}

// Store persists A-leg continuity, allocates B-leg sequence numbers, and records attempt lineage.
type Store interface {
	ResolveALeg(ctx context.Context, continuityKey string) (ALegRecord, error)
	CreateALeg(ctx context.Context, continuityKey string) (ALegRecord, error)
	GetALeg(ctx context.Context, aLegID string) (ALegRecord, error)
	SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error
	NextBLeg(ctx context.Context, aLegID string) (BLegRecord, error)
	RecordAttempt(ctx context.Context, rec lipapi.AttemptRecord) error
	LoadAttempts(ctx context.Context, aLegID string) ([]lipapi.AttemptRecord, error)
}

// MemoryStoreOptions configures the in-memory implementation.
type MemoryStoreOptions struct {
	// TTL after LastSeenAt after which an A-leg is lazily evicted. Zero disables expiry.
	TTL time.Duration
	// Now returns the current time; defaults to time.Now when nil.
	Now func() time.Time
}

// MemoryStore is a mutex-protected in-memory Store with lazy TTL eviction.
type MemoryStore struct {
	ttl  time.Duration
	now  func() time.Time
	mu   sync.RWMutex
	legs map[string]*legState // aLegID -> state
	// continuityKey (non-empty) -> current aLegID for Resolve
	byKey map[string]string
}

var _ Store = (*MemoryStore)(nil)

type legState struct {
	record                ALegRecord
	nextSeq               int
	seqToBLeg             map[int]string
	attemptBySeq          map[int]lipapi.AttemptRecord
	continuityKeyInternal string // same as record.ContinuityKey; used on eviction
}

// NewMemoryStore returns an empty store. opts may be zero-valued defaults.
func NewMemoryStore(opts MemoryStoreOptions) *MemoryStore {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{
		ttl:   opts.TTL,
		now:   now,
		legs:  make(map[string]*legState),
		byKey: make(map[string]string),
	}
}

func (s *MemoryStore) nowTime() time.Time { return s.now() }

func newID(prefix string) string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("b2bua: crypto/rand: " + err.Error())
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

// ResolveALeg returns the active A-leg for a non-empty continuity key, refreshing LastSeenAt.
func (s *MemoryStore) ResolveALeg(ctx context.Context, continuityKey string) (ALegRecord, error) {
	if err := ctx.Err(); err != nil {
		return ALegRecord{}, err
	}
	if continuityKey == "" {
		return ALegRecord{}, ErrInvalidContinuityKey
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowTime()
	aID, ok := s.byKey[continuityKey]
	if !ok {
		return ALegRecord{}, ErrALegNotFound
	}
	st, ok := s.legs[aID]
	if !ok {
		delete(s.byKey, continuityKey)
		return ALegRecord{}, ErrALegNotFound
	}
	if s.evictIfStaleLocked(st, now) {
		return ALegRecord{}, ErrALegNotFound
	}
	st.record.LastSeenAt = now
	return st.record, nil
}

// CreateALeg allocates a new A-leg. Empty continuityKey skips key registration (always-new session).
func (s *MemoryStore) CreateALeg(ctx context.Context, continuityKey string) (ALegRecord, error) {
	if err := ctx.Err(); err != nil {
		return ALegRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowTime()
	aID := newID("a")
	rec := ALegRecord{
		ALegID:        aID,
		ContinuityKey: continuityKey,
		CreatedAt:     now,
		LastSeenAt:    now,
	}
	st := &legState{
		record:                rec,
		seqToBLeg:             make(map[int]string),
		attemptBySeq:          make(map[int]lipapi.AttemptRecord),
		continuityKeyInternal: continuityKey,
	}
	s.legs[aID] = st
	if continuityKey != "" {
		if oldID, ok := s.byKey[continuityKey]; ok && oldID != aID {
			s.removeLegLocked(oldID)
		}
		s.byKey[continuityKey] = aID
	}
	return rec, nil
}

// GetALeg loads an A-leg by id (for clients that already hold ALegID).
func (s *MemoryStore) GetALeg(ctx context.Context, aLegID string) (ALegRecord, error) {
	if err := ctx.Err(); err != nil {
		return ALegRecord{}, err
	}
	if aLegID == "" {
		return ALegRecord{}, ErrALegNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.legs[aLegID]
	if !ok {
		return ALegRecord{}, ErrALegNotFound
	}
	if s.evictIfStaleLocked(st, s.nowTime()) {
		return ALegRecord{}, ErrALegNotFound
	}
	st.record.LastSeenAt = s.nowTime()
	return st.record, nil
}

// SetWeightedFirstConsumed updates session first-request routing state (idempotent).
func (s *MemoryStore) SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.legs[aLegID]
	if !ok {
		return ErrALegNotFound
	}
	if s.evictIfStaleLocked(st, s.nowTime()) {
		return ErrALegNotFound
	}
	st.record.WeightedFirstConsumed = consumed
	st.record.LastSeenAt = s.nowTime()
	return nil
}

// NextBLeg allocates the next monotonic B-leg id and sequence for the A-leg.
func (s *MemoryStore) NextBLeg(ctx context.Context, aLegID string) (BLegRecord, error) {
	if err := ctx.Err(); err != nil {
		return BLegRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.legs[aLegID]
	if !ok {
		return BLegRecord{}, ErrALegNotFound
	}
	now := s.nowTime()
	if s.evictIfStaleLocked(st, now) {
		return BLegRecord{}, ErrALegNotFound
	}
	st.nextSeq++
	seq := st.nextSeq
	bid := newID("b")
	st.seqToBLeg[seq] = bid
	st.record.LastSeenAt = now
	return BLegRecord{BLegID: bid, ALegID: aLegID, Seq: seq}, nil
}

// RecordAttempt upserts one lineage row for (ALegID, Seq). BLegID must match the allocation from NextBLeg.
func (s *MemoryStore) RecordAttempt(ctx context.Context, rec lipapi.AttemptRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if rec.ALegID == "" || rec.Seq <= 0 || rec.BLegID == "" {
		return fmt.Errorf("%w: missing ids or seq", ErrInvalidAttempt)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.legs[rec.ALegID]
	if !ok {
		return ErrALegNotFound
	}
	now := s.nowTime()
	if s.evictIfStaleLocked(st, now) {
		return ErrALegNotFound
	}
	if rec.Seq > st.nextSeq {
		return fmt.Errorf("%w: seq %d beyond allocated next %d", ErrInvalidAttempt, rec.Seq, st.nextSeq)
	}
	wantBLeg, ok := st.seqToBLeg[rec.Seq]
	if !ok || wantBLeg != rec.BLegID {
		return fmt.Errorf("%w: b-leg mismatch for seq %d", ErrInvalidAttempt, rec.Seq)
	}
	st.attemptBySeq[rec.Seq] = rec
	st.record.LastSeenAt = now
	return nil
}

// LoadAttempts returns attempt rows for an A-leg ordered by ascending Seq.
func (s *MemoryStore) LoadAttempts(ctx context.Context, aLegID string) ([]lipapi.AttemptRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.legs[aLegID]
	if !ok {
		return nil, ErrALegNotFound
	}
	if s.evictIfStaleLocked(st, s.nowTime()) {
		return nil, ErrALegNotFound
	}
	st.record.LastSeenAt = s.nowTime()
	out := make([]lipapi.AttemptRecord, 0, len(st.attemptBySeq))
	for _, r := range st.attemptBySeq {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b lipapi.AttemptRecord) int {
		if a.Seq < b.Seq {
			return -1
		}
		if a.Seq > b.Seq {
			return 1
		}
		return 0
	})
	return out, nil
}

func (s *MemoryStore) evictIfStaleLocked(st *legState, now time.Time) bool {
	if s.ttl <= 0 {
		return false
	}
	if now.Sub(st.record.LastSeenAt) < s.ttl {
		return false
	}
	s.removeLegLocked(st.record.ALegID)
	return true
}

func (s *MemoryStore) removeLegLocked(aLegID string) {
	st, ok := s.legs[aLegID]
	if !ok {
		return
	}
	delete(s.legs, aLegID)
	if st.continuityKeyInternal != "" {
		if cur, ok := s.byKey[st.continuityKeyInternal]; ok && cur == aLegID {
			delete(s.byKey, st.continuityKeyInternal)
		}
	}
}
