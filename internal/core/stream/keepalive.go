package stream

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ErrNilEventStream is returned by NewKeepalive when the inner stream is nil.
var ErrNilEventStream = errors.New("stream: NewKeepalive: nil EventStream")

// KeepaliveConfig controls keepalive event injection during idle stream waits.
type KeepaliveConfig struct {
	Interval     time.Duration
	NewKeepalive func() lipapi.Event
}

// KeepaliveEventCode is the WarningCode used for canonical keepalive events.
const KeepaliveEventCode = "keepalive"

// DefaultKeepaliveEvent returns a standard keepalive warning event.
func DefaultKeepaliveEvent() lipapi.Event {
	return lipapi.Event{
		Kind:           lipapi.EventWarning,
		WarningCode:    KeepaliveEventCode,
		WarningMessage: "keepalive",
	}
}

// Keepalive wraps an inner EventStream and emits keepalive events at the configured
// interval while Recv is blocked waiting for the next real event. This prevents
// client-side idle timeouts during recovery waits (Req 5.5).
//
// A single background goroutine sequentially reads from the inner stream,
// preventing concurrent access. Recv returns either a buffered real event or
// a keepalive when the inner stream hasn't produced a result within the interval.
// Close cancels any in-flight inner Recv (see abortRead) and closes k.done so the
// reader loop exits without a second goroutine.
//
// EventStream concurrency: one goroutine calls Recv at a time. Close is safe
// concurrently with Recv blocked on the keepalive select or inner I/O; Close aborts
// the in-flight inner Recv then closes the inner stream.
type Keepalive struct {
	inner lipapi.EventStream
	cfg   KeepaliveConfig

	mu     sync.Mutex
	closed bool

	once         sync.Once
	shutdownOnce sync.Once
	shutdownErr  error
	result       chan item
	done         chan struct{}

	// abortRead cancels the in-flight inner.Recv when the caller's Recv context
	// ends (deadline/cancel) so we do not leak the reader goroutine when Close is
	// never called. Timer-based keepalive returns clear the pending AfterFunc via
	// defer stop() without cancelling the inner read.
	abortMu   sync.Mutex
	abortRead context.CancelFunc
}

var _ lipapi.EventStream = (*Keepalive)(nil)

type item struct {
	ev  lipapi.Event
	err error
}

// preferBufferedItemOrKeepalive returns a buffered inner item when one is already
// available on ch; otherwise it emits a keepalive. Used after the outer select
// chose the keepalive timer while a real event may have become ready concurrently.
func preferBufferedItemOrKeepalive(ch <-chan item, kaFn func() lipapi.Event) (lipapi.Event, error) {
	select {
	case it, ok := <-ch:
		if !ok {
			return lipapi.Event{}, context.Canceled
		}
		return it.ev, it.err
	default:
		return kaFn(), nil
	}
}

// NewKeepalive wraps s with keepalive injection using cfg. It returns ErrNilEventStream if s is nil.
func NewKeepalive(s lipapi.EventStream, cfg KeepaliveConfig) (*Keepalive, error) {
	if s == nil {
		return nil, ErrNilEventStream
	}
	kaFn := cfg.NewKeepalive
	if kaFn == nil {
		kaFn = DefaultKeepaliveEvent
	}
	return &Keepalive{
		inner: s,
		cfg: KeepaliveConfig{
			Interval:     cfg.Interval,
			NewKeepalive: kaFn,
		},
		result: make(chan item, 1),
		done:   make(chan struct{}),
	}, nil
}

func (k *Keepalive) startReader() {
	k.once.Do(func() {
		go func() {
			defer close(k.result)
			for {
				select {
				case <-k.done:
					return
				default:
				}
				innerCtx, cancelInner := context.WithCancel(context.Background())
				k.setAbortRead(cancelInner)
				ev, err := k.inner.Recv(innerCtx)
				k.clearAbortRead()
				select {
				case k.result <- item{ev: ev, err: err}:
				case <-k.done:
					cancelInner()
					return
				}
				if err != nil {
					return
				}
			}
		}()
	})
}

func (k *Keepalive) setAbortRead(cancel context.CancelFunc) {
	k.abortMu.Lock()
	k.abortRead = cancel
	k.abortMu.Unlock()
}

func (k *Keepalive) clearAbortRead() {
	k.abortMu.Lock()
	k.abortRead = nil
	k.abortMu.Unlock()
}

func (k *Keepalive) abortCurrentRead() {
	k.abortMu.Lock()
	fn := k.abortRead
	k.abortRead = nil
	k.abortMu.Unlock()
	if fn != nil {
		fn()
	}
}

// Recv returns the next event from the inner stream, or a keepalive event if the
// inner stream is idle beyond the configured interval.
func (k *Keepalive) Recv(ctx context.Context) (lipapi.Event, error) {
	if err := ctx.Err(); err != nil {
		return lipapi.Event{}, err
	}
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return lipapi.Event{}, context.Canceled
	}
	k.mu.Unlock()

	k.startReader()
	stopAfter := context.AfterFunc(ctx, k.abortCurrentRead)
	defer stopAfter()

	kaFn := k.cfg.NewKeepalive

	timer := time.NewTimer(k.cfg.Interval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		k.abortCurrentRead()
		return lipapi.Event{}, ctx.Err()
	case it, ok := <-k.result:
		if !ok {
			return lipapi.Event{}, context.Canceled
		}
		return it.ev, it.err
	case <-timer.C:
		// If ctx was canceled in the same window as the timer, prefer cancellation
		// over emitting a keepalive.
		select {
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		default:
		}
		// If the inner reader posted a result in the same scheduling window as the
		// timer, Go's select chooses arbitrarily among ready cases. Prefer a buffered
		// real event over a synthetic keepalive for stable stream semantics and tests.
		return preferBufferedItemOrKeepalive(k.result, kaFn)
	}
}

// Close stops the reader goroutine and closes the inner stream.
// Close is idempotent and safe for concurrent callers.
func (k *Keepalive) Close() error {
	k.shutdownOnce.Do(func() {
		k.mu.Lock()
		k.closed = true
		k.mu.Unlock()
		// Unblock an in-flight inner.Recv before closing k.done so the reader does not
		// start another iteration after shutdown (inner Close may be a no-op).
		k.abortCurrentRead()
		close(k.done)
		k.shutdownErr = k.inner.Close()
	})
	return k.shutdownErr
}
