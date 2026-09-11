package largebody

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// ErrSourceClosed is returned when attempting an operation on a closed CompletedSource.
var ErrSourceClosed = errors.New("largebody: source is closed")

// ErrAlreadyCompleted is returned when attempting to complete or write to an already completed SpillBuffer.
var ErrAlreadyCompleted = errors.New("largebody: spill buffer is already completed")

// CompletedSourceConfig configures an immutable CompletedSource (Requirements 10, 20; design section 5).
type CompletedSourceConfig struct {
	// Memory is the in-memory prefix of the captured body (if any).
	Memory []byte

	// FilePath is the path to the spilled temporary file (if any).
	FilePath string

	// Size is the exact captured body size in bytes.
	Size int64

	// Digest is the replay/attempt source integrity digest (Requirements 15, 16, 20; design section 5).
	// If zero and data is in-memory without spill, it is automatically computed via SHA-256.
	Digest SourceDigest

	// Reservation is an optional logical spool reservation released on root close.
	Reservation *SpoolReservation

	// OpenFile optionally overrides opening the spill file for readers (for testing/fault injection).
	OpenFile func(path string) (io.ReadCloser, error)

	// RemoveFile optionally overrides spill file deletion (for testing/fault injection).
	RemoveFile func(path string) error
}

// CompletedSource represents an immutable completed replay capture (Requirements 10, 20; design section 5).
// It implements Source.
//
// Invariants:
//   - Completely immutable: exposes no write or mutation methods.
//   - Independent offset-zero readers: each Open() returns a fresh reader starting at offset zero.
//   - Parallel readers are completely isolated: reading, seeking, or closing one reader has zero effect on others.
//   - Root Close() is idempotent and nonblocking: does not deadlock on active or leaked readers (Requirement 20.9).
//   - Windows open-file deletion safety: if readers are active during root Close(), deletion is marked pending
//     and the spill file is removed only when the last active reader closes (Requirements 20.9, 20.10).
//   - No background cleanup goroutines required.
//   - Binds an immutable SourceDigest for replay/attempt evidence; explicitly not a substitute for canonical semantic identity (Requirements 15, 16.5).
//
// spillLifecycle manages the spill file path, reader refcount, deletion status, and removal.
// It is shared between SpillBuffer and CompletedSource to maintain lifecycle equivalence
// across transitions (Finding M4).
type spillLifecycle struct {
	mu            sync.Mutex
	filePath      string
	activeReaders int
	deletePending bool
	removeFile    func(path string) error
}

func newSpillLifecycle(filePath string, removeFile func(path string) error) *spillLifecycle {
	if removeFile == nil {
		removeFile = os.Remove
	}
	return &spillLifecycle{
		filePath:   filePath,
		removeFile: removeFile,
	}
}

func (l *spillLifecycle) setFilePath(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.filePath = path
}

func (l *spillLifecycle) FilePath() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.filePath
}

func (l *spillLifecycle) HasSpilled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.filePath != ""
}

func (l *spillLifecycle) ActiveReaders() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.activeReaders
}

func (l *spillLifecycle) DeletePending() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.deletePending
}

func (l *spillLifecycle) addReader() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.activeReaders++
}

func (l *spillLifecycle) readerClosed() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.activeReaders--
	if l.activeReaders < 0 {
		l.activeReaders = 0
	}

	if l.deletePending && l.activeReaders == 0 && l.filePath != "" {
		_ = l.removeFile(l.filePath)
		l.filePath = ""
	}
}

func (l *spillLifecycle) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var closeErr error
	if l.filePath != "" {
		if l.activeReaders == 0 {
			if err := l.removeFile(l.filePath); err != nil {
				closeErr = err
			}
			l.filePath = ""
		} else {
			l.deletePending = true
		}
	}
	return closeErr
}

type CompletedSource struct {
	mu sync.Mutex

	mem         []byte
	size        int64
	digest      SourceDigest
	reservation *SpoolReservation
	closed      bool

	lifecycle *spillLifecycle
	openFile  func(path string) (io.ReadCloser, error)
}

var _ Source = (*CompletedSource)(nil)
var _ io.Closer = (*CompletedSource)(nil)

// NewCompletedSource validates configuration and constructs an initialized CompletedSource.
func NewCompletedSource(cfg CompletedSourceConfig) (*CompletedSource, error) {
	if cfg.Size < 0 {
		return nil, fmt.Errorf("largebody: completed source size must be >= 0, got %d", cfg.Size)
	}
	if len(cfg.Memory) == 0 && cfg.FilePath == "" && cfg.Size > 0 {
		return nil, fmt.Errorf("largebody: completed source with non-zero size must have memory or file backing")
	}

	openFile := cfg.OpenFile
	if openFile == nil {
		openFile = defaultOpenSpillFile
	}
	removeFile := cfg.RemoveFile
	if removeFile == nil {
		removeFile = os.Remove
	}

	size := cfg.Size
	var mem []byte
	if len(cfg.Memory) > 0 {
		mem = make([]byte, len(cfg.Memory))
		copy(mem, cfg.Memory)
		if size == 0 {
			size = int64(len(mem))
		}
	}

	digest := cfg.Digest
	if digest.IsZero() && len(mem) > 0 && cfg.FilePath == "" {
		digest = NewSourceDigest(sha256.Sum256(mem))
	}

	return &CompletedSource{
		mem:         mem,
		size:        size,
		digest:      digest,
		reservation: cfg.Reservation,
		openFile:    openFile,
		lifecycle:   newSpillLifecycle(cfg.FilePath, removeFile),
	}, nil
}

// NewMemorySource constructs an immutable in-memory CompletedSource from data.
func NewMemorySource(data []byte) *CompletedSource {
	var mem []byte
	var digest SourceDigest
	if len(data) > 0 {
		mem = make([]byte, len(data))
		copy(mem, data)
		digest = NewSourceDigest(sha256.Sum256(mem))
	}
	return &CompletedSource{
		mem:       mem,
		size:      int64(len(mem)),
		digest:    digest,
		openFile:  defaultOpenSpillFile,
		lifecycle: newSpillLifecycle("", os.Remove),
	}
}

// Size reports the exact captured body size in bytes (implements Source).
func (s *CompletedSource) Size() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}

// Open returns an independent offset-zero reader over the captured body (implements Source).
// Multiple readers can be opened concurrently and read independently.
func (s *CompletedSource) Open() (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, ErrSourceClosed
	}

	path := s.lifecycle.FilePath()
	if path == "" {
		s.lifecycle.addReader()
		return &completedSourceReader{
			lifecycle: s.lifecycle,
			r:         bytes.NewReader(s.mem),
		}, nil
	}

	rc, err := s.openFile(path)
	if err != nil {
		return nil, err
	}

	s.lifecycle.addReader()
	if len(s.mem) > 0 {
		multi := io.MultiReader(bytes.NewReader(s.mem), rc)
		return &completedSourceReader{
			lifecycle: s.lifecycle,
			r:         multi,
			f:         rc,
		}, nil
	}

	return &completedSourceReader{
		lifecycle: s.lifecycle,
		r:         rc,
		f:         rc,
	}, nil
}

// Close releases the completed source (implements io.Closer and Source).
//
// Invariants (Requirement 20.9):
//   - Idempotent and nonblocking.
//   - Releases any associated spool reservation.
//   - If active readers exist, deletion is marked pending and final removal
//     occurs when the last reader closes (Windows open-file safety).
func (s *CompletedSource) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true

	if s.reservation != nil {
		s.reservation.Release()
		s.reservation = nil
	}
	s.mu.Unlock()

	return s.lifecycle.close()
}

func (s *CompletedSource) readerClosed() {
	s.lifecycle.readerClosed()
}

// MemoryBytes returns a copy of the in-memory portion of the source.
func (s *CompletedSource) MemoryBytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.mem) == 0 {
		return nil
	}
	out := make([]byte, len(s.mem))
	copy(out, s.mem)
	return out
}

// FilePath returns the spill file path, or empty string if not spilled.
func (s *CompletedSource) FilePath() string {
	return s.lifecycle.FilePath()
}

// HasSpilled reports whether the source is backed by secondary spill storage.
func (s *CompletedSource) HasSpilled() bool {
	return s.lifecycle.HasSpilled()
}

// ActiveReaders reports the current count of open readers.
func (s *CompletedSource) ActiveReaders() int {
	return s.lifecycle.ActiveReaders()
}

// IsClosed reports whether root Close() has been called.
func (s *CompletedSource) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// IsDeletePending reports whether file deletion is deferred until all readers close.
func (s *CompletedSource) IsDeletePending() bool {
	return s.lifecycle.DeletePending()
}

// Digest reports the bound source integrity digest (Requirements 15, 16, 20; design section 5).
// The source digest is for replay/attempt evidence only and is never a substitute for
// canonical semantic identity (Requirement 16.5).
func (s *CompletedSource) Digest() SourceDigest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.digest
}

// SourceDigest reports the bound source integrity digest (alias for Digest).
func (s *CompletedSource) SourceDigest() SourceDigest {
	return s.Digest()
}

// String returns a bounded diagnostic representation without leaking prompt
// content, model names, or filesystem paths (Requirement 20.3).
func (s *CompletedSource) String() string {
	if s == nil {
		return "<nil>"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("CompletedSource{size=%d, mem=%d, spilled=%t, readers=%d, closed=%t, digest=%s}",
		s.size, len(s.mem), s.lifecycle.HasSpilled(), s.lifecycle.ActiveReaders(), s.closed, s.digest.String())
}

// completedSourceReader wraps an io.Reader and tracks reader lifetime on the parent CompletedSource.
type completedSourceReader struct {
	lifecycle *spillLifecycle
	r         io.Reader
	f         io.Closer
	once      sync.Once
}

func (r *completedSourceReader) Read(p []byte) (int, error) {
	return r.r.Read(p)
}

func (r *completedSourceReader) Close() error {
	var err error
	r.once.Do(func() {
		if r.f != nil {
			err = r.f.Close()
		}
		r.lifecycle.readerClosed()
	})
	return err
}
