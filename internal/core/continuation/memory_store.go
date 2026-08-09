package continuation

import (
	"context"
	"fmt"
	"sync"
	"time"

	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

type scopedKey struct {
	scope lipcont.Scope
	id    lipcont.ResponseID
}

type storedRecord struct {
	record             lipcont.ContinuationRecord
	expiresAt          time.Time
	reservationExpires time.Time
	bytes              int64
}

// MemoryStore is an in-memory ContinuationStore for contract and security tests.
type MemoryStore struct {
	mu       sync.Mutex
	records  map[scopedKey]storedRecord
	reserved map[scopedKey]storedRecord
	now      func() time.Time
	limits   lipcont.StorageLimits
	closed   bool
}

// NewMemoryStore returns an empty in-memory continuation store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		records:  make(map[scopedKey]storedRecord),
		reserved: make(map[scopedKey]storedRecord),
		now:      time.Now,
		limits:   lipcont.DefaultStorageLimits(),
	}
}

// NewMemoryStoreWithLimits creates a bounded in-memory store.
func NewMemoryStoreWithLimits(limits lipcont.StorageLimits) *MemoryStore {
	s := NewMemoryStore()
	s.limits = mergeLimits(limits)
	return s
}

// Close idempotently closes the store, clearing all records and reservations.
func (s *MemoryStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.records = nil
	s.reserved = nil
	return nil
}

// Reserve allocates a scoped proxy response ID.
func (s *MemoryStore) Reserve(ctx context.Context, scope lipcont.Scope, policy lipcont.StoragePolicy) (lipcont.ResponseID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if scope.IsZero() {
		return "", lipcont.ErrPreviousResponseNotFound
	}
	if err := policy.Validate(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", lipcont.ErrStoreClosed
	}
	s.cleanupLocked(s.now())
	exp := s.expiry(policy)
	if s.limits.MaxRecords > 0 && len(s.records)+len(s.reserved) >= s.limits.MaxRecords {
		return "", lipcont.ErrStorageLimitExceeded
	}

	var id lipcont.ResponseID
	var err error
	for range 5 {
		id, err = NewResponseID(ctx)
		if err != nil {
			return "", err
		}
		key := scopedKey{scope: scope, id: id}
		if _, exists := s.reserved[key]; exists {
			continue
		}
		if _, exists := s.records[key]; exists {
			continue
		}
		s.reserved[key] = storedRecord{
			record: lipcont.ContinuationRecord{
				ID:     id,
				Scope:  scope,
				Policy: policy,
			},
			expiresAt:          exp,
			reservationExpires: reservationExpiry(s.now(), policy),
		}
		return id, nil
	}
	return "", fmt.Errorf("continuation: failed to generate unique response id after retries")
}

// PutTerminal stores a completed continuation record for scoped lookup.
func (s *MemoryStore) PutTerminal(ctx context.Context, record lipcont.ContinuationRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.Scope.IsZero() || record.ID.IsZero() {
		return lipcont.ErrPreviousResponseNotFound
	}
	if !record.Terminal {
		return lipcont.ErrRecordNotReady
	}
	if err := record.ID.Validate(); err != nil {
		return lipcont.ErrPreviousResponseNotFound
	}
	key := scopedKey{scope: record.Scope, id: record.ID}
	stored := lipcont.CloneRecord(record)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return lipcont.ErrStoreClosed
	}
	s.cleanupLocked(s.now())
	reservation, reserved := s.reserved[key]
	if !reserved {
		return lipcont.ErrPreviousResponseNotFound
	}
	// Reservation policy and expiry are authoritative; callers cannot extend retention.
	stored.Policy = reservation.record.Policy
	exp := reservation.expiresAt
	if len(stored.NativeRefs) > 0 {
		return lipcont.ErrNativeReferencesUnprotected
	}
	if !record.ExpiresAt.IsZero() && (exp.IsZero() || record.ExpiresAt.Before(exp)) {
		exp = record.ExpiresAt
	}
	if stored.Status == lipcont.RecordStatusIncomplete && !stored.Policy.AllowIncomplete {
		return lipcont.ErrIncompleteNotEligible
	}
	if stored.Status == lipcont.RecordStatusFailed {
		return lipcont.ErrRecordNotEligible
	}
	if stored.ChainDepth > s.limits.MaxChainDepth {
		return lipcont.ErrChainDepthExceeded
	}
	bytes := lipcont.RecordSize(stored)
	if s.limits.MaxRecordBytes > 0 && bytes > s.limits.MaxRecordBytes {
		return lipcont.ErrStorageLimitExceeded
	}
	if s.limits.MaxBytes > 0 && s.currentBytesLocked()+bytes > s.limits.MaxBytes {
		return lipcont.ErrStorageLimitExceeded
	}
	delete(s.reserved, key)
	s.records[key] = storedRecord{record: stored, expiresAt: exp, bytes: bytes}
	return nil
}

// Get loads a terminal record for the authoritative scope.
func (s *MemoryStore) Get(ctx context.Context, scope lipcont.Scope, id lipcont.ResponseID) (lipcont.ContinuationRecord, error) {
	if err := ctx.Err(); err != nil {
		return lipcont.ContinuationRecord{}, err
	}
	if scope.IsZero() || id.IsZero() {
		return lipcont.ContinuationRecord{}, lipcont.ErrPreviousResponseNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return lipcont.ContinuationRecord{}, lipcont.ErrStoreClosed
	}
	now := s.now()
	key := scopedKey{scope: scope, id: id}
	if rec, ok := s.records[key]; ok {
		if !rec.expiresAt.IsZero() && !now.Before(rec.expiresAt) {
			delete(s.records, key)
			return lipcont.ContinuationRecord{}, lipcont.ErrPreviousResponseNotFound
		}
		return lipcont.CloneRecord(rec.record), nil
	}
	if _, ok := s.reserved[key]; ok {
		return lipcont.ContinuationRecord{}, lipcont.ErrPreviousResponseNotFound
	}
	return lipcont.ContinuationRecord{}, lipcont.ErrPreviousResponseNotFound
}

func (s *MemoryStore) cleanupLocked(now time.Time) {
	for k, rec := range s.records {
		if !rec.expiresAt.IsZero() && !now.Before(rec.expiresAt) {
			delete(s.records, k)
		}
	}
	for k, rec := range s.reserved {
		if !rec.reservationExpires.IsZero() && !now.Before(rec.reservationExpires) {
			delete(s.reserved, k)
		}
	}
}

// Delete removes a record idempotently within the authoritative scope.
func (s *MemoryStore) Delete(ctx context.Context, scope lipcont.Scope, id lipcont.ResponseID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return lipcont.ErrStoreClosed
	}
	key := scopedKey{scope: scope, id: id}
	delete(s.records, key)
	delete(s.reserved, key)
	return nil
}

func (s *MemoryStore) currentBytesLocked() int64 {
	var total int64
	for _, rec := range s.records {
		total += rec.bytes
	}
	return total
}

func mergeLimits(limits lipcont.StorageLimits) lipcont.StorageLimits {
	defaults := lipcont.DefaultStorageLimits()
	if limits.MaxRecords == 0 {
		limits.MaxRecords = defaults.MaxRecords
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = defaults.MaxBytes
	}
	if limits.MaxRecordBytes == 0 {
		limits.MaxRecordBytes = defaults.MaxRecordBytes
	}
	if limits.MaxChainDepth == 0 {
		limits.MaxChainDepth = defaults.MaxChainDepth
	}
	return limits
}

func (s *MemoryStore) expiry(policy lipcont.StoragePolicy) time.Time {
	if policy.TTL <= 0 {
		return time.Time{}
	}
	return s.now().Add(policy.TTL)
}

const defaultReservationTTL = 24 * time.Hour

func reservationExpiry(now time.Time, policy lipcont.StoragePolicy) time.Time {
	if policy.TTL > 0 {
		return now.Add(policy.TTL)
	}
	return now.Add(defaultReservationTTL)
}

// SetClock replaces the store clock (tests only).
func (s *MemoryStore) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

var _ lipcont.Store = (*MemoryStore)(nil)
