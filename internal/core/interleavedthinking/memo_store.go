package interleavedthinking

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
)

// Sentinel errors for the memo store contract.
var (
	// ErrMemoNotFound is returned when a memo reference does not exist under
	// the requested scope. Cross-scope access must surface this error so
	// session isolation is enforced by the store contract.
	ErrMemoNotFound = errors.New("interleavedthinking: memo not found")
	// ErrMemoTooLarge is returned when a put or update would store memo content
	// exceeding the configured byte limit.
	ErrMemoTooLarge = errors.New("interleavedthinking: memo too large")
	// ErrEmptyScope is returned when a mutating or lookup operation is called
	// with an empty scope. An empty scope cannot own a memo.
	ErrEmptyScope = errors.New("interleavedthinking: empty scope")
	// ErrEmptyMemoRef is returned when a mutating or lookup operation is called
	// with a memo reference whose key is empty. Lookup is keyed by ref.Key, so
	// an empty key cannot identify a memo.
	ErrEmptyMemoRef = errors.New("interleavedthinking: empty memo ref")
)

// Scope identifies the authoritative session or A-leg that owns a memo. Memos
// stored under one scope are not visible from any other scope.
type Scope string

// MemoStore is the core-owned bounded memo store contract keyed by memo
// reference under an authoritative scope.
//
// Memo durability is intentionally implementation-defined. The standard runtime
// wires a process-local in-memory store, so persisted MemoRefs may point at a
// missing memo after restart; callers must treat a missing memo as "no memo".
//
// Lookup is keyed by ref.Key only; Version is not matched on lookup. Every
// mutation that bumps the stored version returns the updated MemoRef so
// callers can observe the current revision and avoid holding stale refs. All
// methods honor context cancellation: if ctx is canceled before work begins,
// the method returns ctx.Err() without mutating store state.
type MemoStore interface {
	Put(ctx context.Context, scope Scope, state MemoState) (interleavedstate.MemoRef, error)
	Get(ctx context.Context, scope Scope, ref interleavedstate.MemoRef) (MemoState, bool, error)
	Update(ctx context.Context, scope Scope, ref interleavedstate.MemoRef, state MemoState) (interleavedstate.MemoRef, error)
	Delete(ctx context.Context, scope Scope, ref interleavedstate.MemoRef) error
}

type memoEntry struct {
	state MemoState
	ref   interleavedstate.MemoRef
}

// InMemoryMemoStore is a process-local, bounded MemoStore keyed by scope and
// memo reference. It is safe for concurrent use.
type InMemoryMemoStore struct {
	mu       sync.Mutex
	maxBytes int
	nextKey  int64
	byScope  map[Scope]map[string]*memoEntry
}

// Compile-time interface assertion.
var _ MemoStore = (*InMemoryMemoStore)(nil)

// NewMemoStore returns an in-memory memo store that rejects memo content larger
// than maxBytes. A non-positive maxBytes disables size enforcement.
func NewMemoStore(maxBytes int) *InMemoryMemoStore {
	return &InMemoryMemoStore{
		maxBytes: maxBytes,
		byScope:  make(map[Scope]map[string]*memoEntry),
	}
}

func (s *InMemoryMemoStore) checkSize(body string) error {
	if s.maxBytes <= 0 {
		return nil
	}
	if len(body) > s.maxBytes {
		return fmt.Errorf("%w: %d > %d", ErrMemoTooLarge, len(body), s.maxBytes)
	}
	return nil
}

func (s *InMemoryMemoStore) scopeMap(scope Scope) map[string]*memoEntry {
	m, ok := s.byScope[scope]
	if !ok {
		m = make(map[string]*memoEntry)
		s.byScope[scope] = m
	}
	return m
}

func (s *InMemoryMemoStore) lookup(scope Scope, ref interleavedstate.MemoRef) (*memoEntry, bool) {
	m, ok := s.byScope[scope]
	if !ok {
		return nil, false
	}
	e, ok := m[ref.Key]
	return e, ok
}

// validateScopeRef checks scope and ref inputs for mutating/lookup operations.
// It does not check context; callers check ctx.Err() first.
func validateScopeRef(scope Scope, ref interleavedstate.MemoRef) error {
	if scope == "" {
		return ErrEmptyScope
	}
	if ref.Key == "" {
		return ErrEmptyMemoRef
	}
	return nil
}

// Put stores a memo under the given scope and returns a fresh memo reference.
// It rejects an empty scope.
func (s *InMemoryMemoStore) Put(ctx context.Context, scope Scope, state MemoState) (interleavedstate.MemoRef, error) {
	if err := ctx.Err(); err != nil {
		return interleavedstate.MemoRef{}, err
	}
	if scope == "" {
		return interleavedstate.MemoRef{}, ErrEmptyScope
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkSize(state.Memo); err != nil {
		return interleavedstate.MemoRef{}, err
	}
	s.nextKey++
	ref := interleavedstate.MemoRef{Key: fmt.Sprintf("memo-%d", s.nextKey), Version: 1}
	s.scopeMap(scope)[ref.Key] = &memoEntry{state: state, ref: ref}
	return ref, nil
}

// Get returns a copy of the memo state for ref under scope, or ok=false if no
// such memo exists under that scope. Lookup is keyed by ref.Key only.
func (s *InMemoryMemoStore) Get(ctx context.Context, scope Scope, ref interleavedstate.MemoRef) (MemoState, bool, error) {
	if err := ctx.Err(); err != nil {
		return MemoState{}, false, err
	}
	if err := validateScopeRef(scope, ref); err != nil {
		return MemoState{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.lookup(scope, ref)
	if !ok {
		return MemoState{}, false, nil
	}
	return e.state, true, nil
}

// Update replaces the memo state for ref under scope, bumping its version, and
// returns the updated memo reference. Lookup is keyed by ref.Key only.
func (s *InMemoryMemoStore) Update(ctx context.Context, scope Scope, ref interleavedstate.MemoRef, state MemoState) (interleavedstate.MemoRef, error) {
	if err := ctx.Err(); err != nil {
		return interleavedstate.MemoRef{}, err
	}
	if err := validateScopeRef(scope, ref); err != nil {
		return interleavedstate.MemoRef{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.lookup(scope, ref)
	if !ok {
		return interleavedstate.MemoRef{}, ErrMemoNotFound
	}
	if err := s.checkSize(state.Memo); err != nil {
		return interleavedstate.MemoRef{}, err
	}
	e.state = state
	e.ref.Version++
	return e.ref, nil
}

// Delete removes the memo for ref under scope. It is idempotent for a missing
// key, but rejects an empty scope or empty ref as invalid input.
func (s *InMemoryMemoStore) Delete(ctx context.Context, scope Scope, ref interleavedstate.MemoRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateScopeRef(scope, ref); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.byScope[scope]; ok {
		delete(m, ref.Key)
	}
	return nil
}
