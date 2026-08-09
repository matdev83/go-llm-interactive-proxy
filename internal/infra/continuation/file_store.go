// Package continuation contains the small durable continuation adapter used by
// the standard contract tests and local deployments. It owns no goroutines.
package continuation

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

type fileKey struct {
	Scope lipcont.Scope
	ID    lipcont.ResponseID
}

type fileEntry struct {
	Record             lipcont.ContinuationRecord `json:"record"`
	ExpiresAt          time.Time                  `json:"expires_at,omitempty"`
	ReservationExpires time.Time                  `json:"reservation_expires_at,omitempty"`
	Bytes              int64                      `json:"bytes,omitempty"`
}

type fileState struct {
	Records  []fileEntry `json:"records,omitempty"`
	Reserved []fileEntry `json:"reserved,omitempty"`
}

// FileStore is a synchronous, restart-capable Store backed by one atomically
// replaced JSON file. It is intentionally not a general database. The file
// requests mode 0600; Windows ACL inheritance is a deployment responsibility,
// so this adapter does not claim native-reference confidentiality.
type FileStore struct {
	mu     sync.Mutex
	path   string
	lock   string
	state  fileState
	limits lipcont.StorageLimits
	now    func() time.Time
	closed bool
}

// fileStoreLockAcquireTimeout bounds the startup lock wait. The per-operation
// acquire paths are request-context bounded; the constructor has no request
// context, so without a deadline a hung peer lock holder would retry forever.
const fileStoreLockAcquireTimeout = 10 * time.Second

// NewFileStore opens or creates path and loads all previously committed state.
func NewFileStore(path string, limits lipcont.StorageLimits) (*FileStore, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: empty path", lipcont.ErrStorageFailure)
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: path must be absolute", lipcont.ErrStorageFailure)
	}
	if hasParentTraversal(path) {
		return nil, fmt.Errorf("%w: path traversal", lipcont.ErrStorageFailure)
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: store path is a symlink", lipcont.ErrStorageFailure)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: stat: %v", lipcont.ErrStorageFailure, err)
	}
	if err := rejectSymlinkParents(filepath.Dir(path)); err != nil {
		return nil, err
	}
	s := &FileStore{path: path, lock: lockPath(path), limits: mergeLimits(limits), now: time.Now}
	lockCtx, cancelLock := context.WithTimeout(context.Background(), fileStoreLockAcquireTimeout)
	defer cancelLock()
	lock, err := acquireFileLock(lockCtx, s.lock)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.close() }()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := s.persistLocked(context.Background(), fileState{}); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read: %v", lipcont.ErrStorageFailure, err)
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", lipcont.ErrStorageFailure, err)
	}
	normalizeReservations(s.state.Reserved, s.now())
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("%w: permissions: %v", lipcont.ErrStorageFailure, err)
	}
	if err := validateState(s.state, s.limits); err != nil {
		return nil, err
	}
	s.cleanupLocked(s.now())
	if err := s.persistLocked(context.Background(), s.state); err != nil {
		return nil, err
	}
	return s, nil
}

// Close idempotently closes the store and rejects subsequent operations.
func (s *FileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.state = fileState{}
	// Keep the lock inode in place. Removing it while another FileStore instance
	// still holds an open descriptor could allow a split-brain lock after restart.
	return nil
}

// Reserve allocates and durably records a scoped proxy ID.
func (s *FileStore) Reserve(ctx context.Context, scope lipcont.Scope, policy lipcont.StoragePolicy) (lipcont.ResponseID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if scope.IsZero() {
		return "", lipcont.ErrPreviousResponseNotFound
	}
	if err := policy.Validate(); err != nil {
		return "", err
	}
	if policy.Mode == lipcont.PersistenceConnection {
		return "", lipcont.ErrInvalidPolicy
	}
	lock, err := acquireFileLock(ctx, s.lock)
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.close() }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", lipcont.ErrStoreClosed
	}
	if err := s.reloadLocked(); err != nil {
		return "", err
	}
	s.cleanupLocked(s.now())
	if s.limits.MaxRecords > 0 && len(s.state.Records)+len(s.state.Reserved) >= s.limits.MaxRecords {
		return "", lipcont.ErrStorageLimitExceeded
	}
	var id lipcont.ResponseID
	for range 5 {
		id, err = newID(ctx)
		if err != nil {
			return "", err
		}
		if find(s.state.Records, fileKey{scope, id}) < 0 && find(s.state.Reserved, fileKey{scope, id}) < 0 {
			break
		}
		id = ""
	}
	if id.IsZero() {
		return "", fmt.Errorf("continuation: failed to generate unique response id after retries")
	}
	entry := fileEntry{
		Record:             lipcont.ContinuationRecord{ID: id, Scope: scope, Policy: policy},
		ExpiresAt:          expiry(s.now(), policy),
		ReservationExpires: reservationExpiry(s.now(), policy),
	}
	candidate := cloneState(s.state)
	candidate.Reserved = append(candidate.Reserved, entry)
	if err := s.persistLocked(ctx, candidate); err != nil {
		return "", err
	}
	s.state = candidate
	return id, nil
}

// PutTerminal commits a completed eligible record and consumes its reservation.
func (s *FileStore) PutTerminal(ctx context.Context, record lipcont.ContinuationRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.Scope.IsZero() || record.ID.IsZero() || !record.Terminal {
		return lipcont.ErrRecordNotReady
	}
	if err := record.ID.Validate(); err != nil {
		return lipcont.ErrPreviousResponseNotFound
	}
	lock, err := acquireFileLock(ctx, s.lock)
	if err != nil {
		return err
	}
	defer func() { _ = lock.close() }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return lipcont.ErrStoreClosed
	}
	if err := s.reloadLocked(); err != nil {
		return err
	}
	s.cleanupLocked(s.now())
	index := find(s.state.Reserved, fileKey{record.Scope, record.ID})
	if index < 0 {
		return lipcont.ErrPreviousResponseNotFound
	}
	reservation := s.state.Reserved[index]
	if !record.Scope.Equal(reservation.Record.Scope) {
		return lipcont.ErrPreviousResponseNotFound
	}
	stored := lipcont.CloneRecord(record)
	stored.Policy = reservation.Record.Policy
	if len(stored.NativeRefs) > 0 {
		return lipcont.ErrNativeReferencesUnprotected
	}
	if stored.ChainDepth > s.limits.MaxChainDepth {
		return lipcont.ErrChainDepthExceeded
	}
	if stored.Status == lipcont.RecordStatusIncomplete && !stored.Policy.AllowIncomplete {
		return lipcont.ErrIncompleteNotEligible
	}
	if stored.Status == lipcont.RecordStatusFailed {
		return lipcont.ErrRecordNotEligible
	}
	bytes := lipcont.RecordSize(stored)
	if s.limits.MaxRecordBytes > 0 && bytes > s.limits.MaxRecordBytes {
		return lipcont.ErrStorageLimitExceeded
	}
	if s.limits.MaxBytes > 0 && currentBytes(s.state.Records)+bytes > s.limits.MaxBytes {
		return lipcont.ErrStorageLimitExceeded
	}
	expiresAt := reservation.ExpiresAt
	if !record.ExpiresAt.IsZero() && (expiresAt.IsZero() || record.ExpiresAt.Before(expiresAt)) {
		expiresAt = record.ExpiresAt
	}
	entry := fileEntry{Record: stored, ExpiresAt: expiresAt, Bytes: bytes}
	candidate := cloneState(s.state)
	candidate.Reserved = append(candidate.Reserved[:index], candidate.Reserved[index+1:]...)
	candidate.Records = append(candidate.Records, entry)
	if err := s.persistLocked(ctx, candidate); err != nil {
		return err
	}
	s.state = candidate
	return nil
}

// Get performs a scoped terminal lookup with lazy expiry cleanup.
func (s *FileStore) Get(ctx context.Context, scope lipcont.Scope, id lipcont.ResponseID) (lipcont.ContinuationRecord, error) {
	if err := ctx.Err(); err != nil {
		return lipcont.ContinuationRecord{}, err
	}
	lock, err := acquireFileLock(ctx, s.lock)
	if err != nil {
		return lipcont.ContinuationRecord{}, err
	}
	defer func() { _ = lock.close() }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return lipcont.ContinuationRecord{}, lipcont.ErrStoreClosed
	}
	if err := s.reloadLocked(); err != nil {
		return lipcont.ContinuationRecord{}, err
	}
	if s.cleanupLocked(s.now()) {
		if err := s.persistLocked(ctx, s.state); err != nil {
			return lipcont.ContinuationRecord{}, err
		}
	}
	if index := find(s.state.Records, fileKey{scope, id}); index >= 0 {
		return lipcont.CloneRecord(s.state.Records[index].Record), nil
	}
	if find(s.state.Reserved, fileKey{scope, id}) >= 0 {
		return lipcont.ContinuationRecord{}, lipcont.ErrPreviousResponseNotFound
	}
	return lipcont.ContinuationRecord{}, lipcont.ErrPreviousResponseNotFound
}

// Delete removes committed and reserved state idempotently.
func (s *FileStore) Delete(ctx context.Context, scope lipcont.Scope, id lipcont.ResponseID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lock, err := acquireFileLock(ctx, s.lock)
	if err != nil {
		return err
	}
	defer func() { _ = lock.close() }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return lipcont.ErrStoreClosed
	}
	if err := s.reloadLocked(); err != nil {
		return err
	}
	candidate := cloneState(s.state)
	candidate.Records = remove(candidate.Records, fileKey{scope, id})
	candidate.Reserved = remove(candidate.Reserved, fileKey{scope, id})
	if err := s.persistLocked(ctx, candidate); err != nil {
		return err
	}
	s.state = candidate
	return nil
}

func (s *FileStore) cleanupLocked(now time.Time) bool {
	recordCount := len(s.state.Records)
	reservationCount := len(s.state.Reserved)
	s.state.Records = removeExpired(s.state.Records, now)
	s.state.Reserved = removeExpiredReservations(s.state.Reserved, now)
	return len(s.state.Records) != recordCount || len(s.state.Reserved) != reservationCount
}

func (s *FileStore) persistLocked(ctx context.Context, state fileState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("%w: encode: %v", lipcont.ErrStorageFailure, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".continuation-*")
	if err != nil {
		return fmt.Errorf("%w: create temp: %v", lipcont.ErrStorageFailure, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := ctx.Err(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: temp permissions: %v", lipcont.ErrStorageFailure, err)
	}
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: write: %v", lipcont.ErrStorageFailure, err)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("%w: write: %v", lipcont.ErrStorageFailure, err)
	}
	if err := replaceFile(tmpName, s.path); err != nil {
		return fmt.Errorf("%w: rename: %v", lipcont.ErrStorageFailure, err)
	}
	if err := syncDirectory(filepath.Dir(s.path)); err != nil {
		return fmt.Errorf("%w: directory sync: %v", lipcont.ErrStorageFailure, err)
	}
	return nil
}

func (s *FileStore) reloadLocked() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("%w: read: %v", lipcont.ErrStorageFailure, err)
	}
	var state fileState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("%w: decode: %v", lipcont.ErrStorageFailure, err)
	}
	normalizeReservations(state.Reserved, s.now())
	if err := validateState(state, s.limits); err != nil {
		return err
	}
	s.state = state
	return nil
}

func newID(ctx context.Context) (lipcont.ResponseID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var raw [lipcont.MinResponseIDEntropyBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("%w: entropy: %v", lipcont.ErrStorageFailure, err)
	}
	return lipcont.ResponseID(lipcont.ResponseIDPrefix + base64.RawURLEncoding.EncodeToString(raw[:])), nil
}

func expiry(now time.Time, policy lipcont.StoragePolicy) time.Time {
	if policy.TTL <= 0 {
		return time.Time{}
	}
	return now.Add(policy.TTL)
}

const defaultReservationTTL = 24 * time.Hour

func reservationExpiry(now time.Time, policy lipcont.StoragePolicy) time.Time {
	if policy.TTL > 0 {
		return now.Add(policy.TTL)
	}
	return now.Add(defaultReservationTTL)
}

func find(entries []fileEntry, key fileKey) int {
	for i, entry := range entries {
		if entry.Record.Scope.Equal(key.Scope) && entry.Record.ID == key.ID {
			return i
		}
	}
	return -1
}

func remove(entries []fileEntry, key fileKey) []fileEntry {
	if index := find(entries, key); index >= 0 {
		return append(entries[:index], entries[index+1:]...)
	}
	return entries
}

func removeExpired(entries []fileEntry, now time.Time) []fileEntry {
	out := entries[:0]
	for _, entry := range entries {
		if entry.ExpiresAt.IsZero() || now.Before(entry.ExpiresAt) {
			out = append(out, entry)
		}
	}
	return out
}

func removeExpiredReservations(entries []fileEntry, now time.Time) []fileEntry {
	out := entries[:0]
	for _, entry := range entries {
		expiresAt := entry.ReservationExpires
		if expiresAt.IsZero() {
			expiresAt = entry.ExpiresAt
		}
		if expiresAt.IsZero() || now.Before(expiresAt) {
			out = append(out, entry)
		}
	}
	return out
}

func currentBytes(entries []fileEntry) int64 {
	var total int64
	for _, entry := range entries {
		total += entry.Bytes
	}
	return total
}

func cloneState(in fileState) fileState {
	data, err := json.Marshal(in)
	if err != nil {
		// Unreachable: fileState is fully JSON-serializable.
		return in
	}
	var out fileState
	if err := json.Unmarshal(data, &out); err != nil {
		// Unreachable: data was produced by json.Marshal(in) above.
		return in
	}
	return out
}

func validateState(state fileState, limits lipcont.StorageLimits) error {
	if limits.MaxRecords > 0 && len(state.Records)+len(state.Reserved) > limits.MaxRecords {
		return fmt.Errorf("%w: record count", lipcont.ErrStorageFailure)
	}
	if limits.MaxBytes > 0 && currentBytes(state.Records) > limits.MaxBytes {
		return fmt.Errorf("%w: byte count", lipcont.ErrStorageFailure)
	}
	seen := make(map[fileKey]struct{}, len(state.Records)+len(state.Reserved))
	for _, entry := range append(state.Records, state.Reserved...) {
		key := fileKey{Scope: entry.Record.Scope, ID: entry.Record.ID}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: duplicate entry", lipcont.ErrStorageFailure)
		}
		seen[key] = struct{}{}
		if entry.Record.Scope.IsZero() || entry.Record.ID.Validate() != nil {
			return fmt.Errorf("%w: invalid entry", lipcont.ErrStorageFailure)
		}
		if len(entry.Record.NativeRefs) > 0 {
			return fmt.Errorf("%w: native references are not protected", lipcont.ErrStorageFailure)
		}
		if entry.Bytes < 0 || (limits.MaxRecordBytes > 0 && entry.Bytes > limits.MaxRecordBytes) {
			return fmt.Errorf("%w: invalid byte accounting", lipcont.ErrStorageFailure)
		}
		if entry.Record.Terminal && entry.Bytes != lipcont.RecordSize(entry.Record) {
			return fmt.Errorf("%w: invalid byte accounting", lipcont.ErrStorageFailure)
		}
		if !entry.Record.Terminal && entry.ReservationExpires.IsZero() && entry.ExpiresAt.IsZero() {
			return fmt.Errorf("%w: reservation has no expiry", lipcont.ErrStorageFailure)
		}
	}
	return nil
}

func normalizeReservations(entries []fileEntry, now time.Time) {
	for i := range entries {
		if entries[i].ReservationExpires.IsZero() {
			if entries[i].ExpiresAt.IsZero() {
				entries[i].ReservationExpires = now.Add(defaultReservationTTL)
			} else {
				entries[i].ReservationExpires = entries[i].ExpiresAt
			}
		}
	}
}

func hasParentTraversal(path string) bool {
	return slices.Contains(splitPath(path), "..")
}

func rejectSymlinkParents(path string) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: parent path is a symlink", lipcont.ErrStorageFailure)
		}
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("%w: parent stat: %v", lipcont.ErrStorageFailure, err)
		}
		next := filepath.Dir(current)
		if next == current {
			return nil
		}
	}
}

func splitPath(path string) []string {
	path = filepath.ToSlash(path)
	parts := make([]string, 0, 8)
	for part := range strings.SplitSeq(path, "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return parts
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

var _ lipcont.Store = (*FileStore)(nil)
