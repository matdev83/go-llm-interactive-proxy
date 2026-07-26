package acp

import (
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

var ErrPendingQueueFull = errors.New("acp: pending event queue capacity exceeded")

type PendingEventQueue struct {
	buf    []lipapi.Event
	head   int
	maxLen int
}

func NewPendingEventQueue(maxLen int) PendingEventQueue {
	return PendingEventQueue{maxLen: maxLen}
}

func (q *PendingEventQueue) Len() int {
	return len(q.buf) - q.head
}

func (q *PendingEventQueue) Push(ev lipapi.Event) error {
	if q.maxLen > 0 && q.Len() >= q.maxLen {
		return ErrPendingQueueFull
	}
	q.compactIfNeeded()
	q.buf = append(q.buf, ev)
	return nil
}

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
	if q.head < 64 && q.head <= alive {
		return
	}
	if cap(q.buf) > 1024 && alive < cap(q.buf)/4 {
		next := make([]lipapi.Event, alive, alive*2)
		copy(next, q.buf[q.head:])
		q.buf = next
		q.head = 0
		return
	}
	oldLen := len(q.buf)
	copy(q.buf[:alive], q.buf[q.head:])
	clear(q.buf[alive:oldLen])
	q.buf = q.buf[:alive]
	q.head = 0
}
