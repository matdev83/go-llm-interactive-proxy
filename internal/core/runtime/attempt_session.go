package runtime

import (
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// attemptSession is the owner of one opened B-leg. Every field in this type
// has the same lifetime as the backend attempt and is discarded on replacement.
// The inner lock protects only the backend stream pointer; callers snapshot it
// and perform Cancel/Close without holding this lock.
type attemptSession struct {
	innerMu sync.Mutex
	inner   lipapi.ManagedEventStream

	bleg      b2bua.BLegRecord
	cand      routing.AttemptCandidate
	authority authorityLifecycle
	terminal  *streamTerminal
}

// attemptSessionInput keeps attempt construction explicit. It deliberately
// does not expose a generic state bag: each owner is initialized from one
// attemptOpenResult and its backend-specific controls.
type attemptSessionInput struct {
	inner     lipapi.ManagedEventStream
	bleg      b2bua.BLegRecord
	cand      routing.AttemptCandidate
	authority authorityLifecycle
}

func newAttemptSession(in attemptSessionInput) *attemptSession {
	return &attemptSession{
		inner:     in.inner,
		bleg:      in.bleg,
		cand:      in.cand,
		authority: in.authority,
		terminal:  newStreamTerminal(sdkterminal.ScopeAttempt),
	}
}

func (a *attemptSession) loadInner() lipapi.ManagedEventStream {
	if a == nil {
		return nil
	}
	a.innerMu.Lock()
	defer a.innerMu.Unlock()
	return a.inner
}

func (a *attemptSession) storeInner(stream lipapi.ManagedEventStream) {
	if a == nil {
		return
	}
	a.innerMu.Lock()
	a.inner = stream
	a.innerMu.Unlock()
}

func (a *attemptSession) takeInner() lipapi.ManagedEventStream {
	if a == nil {
		return nil
	}
	a.innerMu.Lock()
	defer a.innerMu.Unlock()
	inner := a.inner
	a.inner = nil
	return inner
}

// attemptSlot protects only the current attempt pointer. It never holds its
// lock while receiving, cancelling, closing, terminalizing, or persisting.
type attemptSlot struct {
	mu      sync.Mutex
	current *attemptSession
}

func (s *attemptSlot) snapshot() *attemptSession {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// require returns the installed attempt. Production stream construction and
// replacement install a complete session before exposing the stream; a
// missing session is therefore an internal construction error.
func (s *attemptSlot) require() *attemptSession {
	if s == nil {
		panic("runtime: attempt slot is nil")
	}
	if attempt := s.snapshot(); attempt != nil {
		return attempt
	}
	panic("runtime: attempt session not installed")
}

func (s *attemptSlot) install(next *attemptSession) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.current = next
	s.mu.Unlock()
}

func (s *attemptSlot) swap(next *attemptSession) (old *attemptSession) {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	old, s.current = s.current, next
	s.mu.Unlock()
	return old
}
