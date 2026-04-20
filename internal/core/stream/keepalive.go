package stream

import (
	"context"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

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
type Keepalive struct {
	inner lipapi.EventStream
	cfg   KeepaliveConfig

	mu     sync.Mutex
	closed bool

	once   sync.Once
	result chan item
	done   chan struct{}
}

type item struct {
	ev  lipapi.Event
	err error
}

// NewKeepalive wraps s with keepalive injection using cfg.
func NewKeepalive(s lipapi.EventStream, cfg KeepaliveConfig) *Keepalive {
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
	}
}

func (k *Keepalive) startReader() {
	k.once.Do(func() {
		go func() {
			defer close(k.result)
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				<-k.done
				cancel()
			}()
			for {
				ev, err := k.inner.Recv(ctx)
				select {
				case k.result <- item{ev: ev, err: err}:
				case <-k.done:
					return
				}
				if err != nil {
					return
				}
			}
		}()
	})
}

// Recv returns the next event from the inner stream, or a keepalive event if the
// inner stream is idle beyond the configured interval.
func (k *Keepalive) Recv(ctx context.Context) (lipapi.Event, error) {
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return lipapi.Event{}, context.Canceled
	}
	k.mu.Unlock()

	k.startReader()
	kaFn := k.cfg.NewKeepalive

	timer := time.NewTimer(k.cfg.Interval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return lipapi.Event{}, ctx.Err()
	case it, ok := <-k.result:
		if !ok {
			return lipapi.Event{}, context.Canceled
		}
		return it.ev, it.err
	case <-timer.C:
		return kaFn(), nil
	}
}

// Close stops the reader goroutine and closes the inner stream.
func (k *Keepalive) Close() error {
	k.mu.Lock()
	k.closed = true
	k.mu.Unlock()
	close(k.done)
	return k.inner.Close()
}
