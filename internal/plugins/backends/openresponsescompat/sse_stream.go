package openresponsescompat

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// sseReadBufferSize is the bufio reader buffer used for one SSE response body.
// Lines longer than the buffer are still handled by the bounded line reader,
// so this size only affects read amplification, not the accepted payload size.
const sseReadBufferSize = 64 << 10

// sseStream is the canonical managed stream for one remote SSE create response.
// Reads are strictly pull-driven: each Recv consumes at most one SSE record and
// maps it to at most a few canonical events, so a slow consumer never causes
// unbounded read-ahead and the pending buffer stays bounded. Close closes the
// upstream response body exactly once and unblocks any in-flight Recv.
type sseStream struct {
	id     string
	br     *bufio.Reader
	body   io.ReadCloser
	limits ResponseLimits
	mapper *streamMapper

	maxPending int

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	deferred  []lipapi.Event
}

var (
	_ lipapi.EventStream        = (*sseStream)(nil)
	_ lipapi.ManagedEventStream = (*sseStream)(nil)
)

// newSSEStream constructs the managed stream. maxPending bounds how many
// canonical events one Recv may buffer ahead (0 = unlimited, the default).
func newSSEStream(id string, body io.ReadCloser, limits ResponseLimits, maxPending int) *sseStream {
	if limits.MaxEventBytes <= 0 {
		limits.MaxEventBytes = DefaultMaxResponseEventBytes
	}
	return &sseStream{
		id:         id,
		br:         bufio.NewReaderSize(body, sseReadBufferSize),
		body:       body,
		limits:     limits,
		mapper:     newStreamMapper(id, limits),
		maxPending: maxPending,
	}
}

func (s *sseStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return lipapi.Event{}, err
	}
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return lipapi.Event{}, io.EOF
		}
		if len(s.deferred) > 0 {
			ev := s.deferred[0]
			s.deferred = s.deferred[1:]
			s.mu.Unlock()
			return ev, nil
		}
		s.mu.Unlock()

		rec, err := s.nextRecord()
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.mu.Lock()
				closed, terminal, done := s.closed, s.mapper.terminal, s.mapper.sawDone
				s.mu.Unlock()
				if closed {
					return lipapi.Event{}, io.EOF
				}
				if terminal && done {
					return lipapi.Event{}, io.EOF
				}
				if !terminal {
					return lipapi.Event{}, fmt.Errorf("%s: %w: SSE stream ended before a terminal response event", s.id, ErrMalformedResponse)
				}
				return lipapi.Event{}, fmt.Errorf("%s: %w: SSE stream ended before [DONE]", s.id, ErrMalformedResponse)
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return lipapi.Event{}, err
			}
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return lipapi.Event{}, io.EOF
			}
			return lipapi.Event{}, fmt.Errorf("%s: read SSE stream: %w", s.id, err)
		}

		events, err := s.mapper.mapRecord(rec)
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return lipapi.Event{}, io.EOF
			}
			return lipapi.Event{}, err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return lipapi.Event{}, io.EOF
		}
		if s.maxPending > 0 && len(s.deferred)+len(events) > s.maxPending {
			// The full mapped batch must fit atomically. Rejecting mid-batch
			// would leave a partially buffered batch after the error, surfacing
			// rejected events out of order on later Recvs.
			s.mu.Unlock()
			return lipapi.Event{}, fmt.Errorf("%s: %w", s.id, stream.ErrPendingQueueFull)
		}
		s.deferred = append(s.deferred, events...)
		s.mu.Unlock()
	}
}

// nextRecord reads and parses one SSE record. A read error caused by Close is
// reported as io.EOF so a cancelled stream drains as a normal end.
func (s *sseStream) nextRecord() (sseRecord, error) {
	rec, err := nextSSERecord(s.br, s.limits.MaxEventBytes)
	if err != nil && !errors.Is(err, io.EOF) {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return sseRecord{}, io.EOF
		}
	}
	return rec, err
}

func (s *sseStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	var err error
	s.closeOnce.Do(func() {
		if s.body != nil {
			err = s.body.Close()
		}
	})
	return err
}

func (s *sseStream) Cancel(_ context.Context, _ lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeTransport, Err: s.Close()}
}
