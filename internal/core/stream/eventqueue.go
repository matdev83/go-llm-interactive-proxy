package stream

import (
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ErrPendingQueueFull is returned when [PendingEventQueue.Push] would exceed a configured max length.
var ErrPendingQueueFull = errors.New("stream: pending event queue capacity exceeded")

// PendingEventQueue buffers canonical events for adapters that translate one wire
// chunk into zero or more lipapi.Event values. It avoids slice-prefix dequeue
// (pending = pending[1:]) which retains a large backing array over long streams.
//
// When constructed with a positive max length (see [NewPendingEventQueue]), Push returns
// [ErrPendingQueueFull] once the queue would exceed that cap. When max length is zero
// (default), the queue is unbounded until other request limits apply.
type PendingEventQueue struct {
	buf    []lipapi.Event
	head   int
	maxLen int
}

// NewPendingEventQueue returns a queue with the given max pending events (0 = unlimited).
func NewPendingEventQueue(maxLen int) PendingEventQueue {
	return PendingEventQueue{maxLen: maxLen}
}

// Len returns the number of queued events.
func (q *PendingEventQueue) Len() int {
	return len(q.buf) - q.head
}

// Push appends an event to the tail.
func (q *PendingEventQueue) Push(ev lipapi.Event) error {
	if q.maxLen > 0 && q.Len() >= q.maxLen {
		return ErrPendingQueueFull
	}
	q.compactIfNeeded()
	q.buf = append(q.buf, ev)
	return nil
}

// PopFront removes and returns the oldest event. The second result is false when empty.
func (q *PendingEventQueue) PopFront() (lipapi.Event, bool) {
	if len(q.buf) <= q.head {
		q.buf = q.buf[:0]
		q.head = 0
		return lipapi.Event{}, false
	}
	ev := q.buf[q.head]
	q.head++
	q.compactIfNeeded()
	return ev, true
}

// DrainPending pops every queued event in order and returns them. The queue is empty afterward.
func DrainPending(q *PendingEventQueue) []lipapi.Event {
	out := make([]lipapi.Event, 0, q.Len())
	for {
		ev, ok := q.PopFront()
		if !ok {
			return out
		}
		out = append(out, ev)
	}
}

func (q *PendingEventQueue) compactIfNeeded() {
	alive := len(q.buf) - q.head
	if q.head == 0 {
		if alive == 0 && cap(q.buf) > 0 {
			q.buf = q.buf[:0]
		}
		return
	}
	if alive == 0 {
		q.buf = q.buf[:0]
		q.head = 0
		return
	}
	// Compact when the dead prefix is large or dominates the live tail.
	if q.head < 64 && q.head <= alive {
		return
	}
	if alive <= cap(q.buf)/2 && cap(q.buf) > 32 {
		copy(q.buf[:alive], q.buf[q.head:])
		q.buf = q.buf[:alive]
		q.head = 0
		return
	}
	next := make([]lipapi.Event, alive, alive+alive/4)
	copy(next, q.buf[q.head:])
	q.buf = next
	q.head = 0
}
