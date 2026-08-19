package runtime

import (
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// attemptSession is the owner of one opened B-leg. Every field in this type
// has the same lifetime as the backend attempt and is discarded on replacement.
// The inner lock protects only the backend stream pointer; callers snapshot it
// and perform Cancel/Close without holding this lock.
type attemptSession struct {
	innerMu   sync.Mutex
	inner     lipapi.ManagedEventStream
	usageMu   sync.Mutex
	billingMu sync.Mutex

	internalUsageKeys  map[string]struct{}
	billingLegRecorded bool

	bleg      b2bua.BLegRecord
	cand      routing.AttemptCandidate
	authority authorityLifecycle
	terminal  *streamTerminal

	accounting            attemptAccountingTracker
	toolFinal             *toolCallAssembler
	promptCacheSource     promptcache.ObservationSource
	promptCacheController promptcache.Controller
	finalStreamObs        *extensions.FinalStreamObservationSession
}

// claimBillingLegRecord is scoped to one attemptSession, which represents one
// B-leg. It prevents overlapping request/attempt terminal effects from
// appending more than one leg record while allowing a replacement attempt its
// own independent record. The claim is retained even if a downstream observer
// or append fails, matching the former stream-level dedupe mark-before-append
// behavior; call-closure retry remains independently owned by turnTerminal.
func (a *attemptSession) claimBillingLegRecord() bool {
	if a == nil {
		return false
	}
	a.billingMu.Lock()
	defer a.billingMu.Unlock()
	if a.billingLegRecorded {
		return false
	}
	a.billingLegRecorded = true
	return true
}

// rememberUsageEvidenceOnce keeps provider sideband dedupe scoped to the B-leg
// that produced it. A replacement attempt owns an independent key set.
func (a *attemptSession) rememberUsageEvidenceOnce(ev lipapi.Event) bool {
	if a == nil || ev.Accounting.DedupeKey == "" {
		return false
	}
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	if a.internalUsageKeys == nil {
		a.internalUsageKeys = make(map[string]struct{})
	}
	key := ev.Accounting.DedupeKey
	if _, exists := a.internalUsageKeys[key]; exists {
		return false
	}
	a.internalUsageKeys[key] = struct{}{}
	return true
}

// attemptSessionInput keeps attempt construction explicit. It deliberately
// does not expose a generic state bag: each owner is initialized from one
// attemptOpenResult and its backend-specific controls.
type attemptSessionInput struct {
	inner     lipapi.ManagedEventStream
	bleg      b2bua.BLegRecord
	cand      routing.AttemptCandidate
	authority authorityLifecycle

	accounting            attemptAccountingTracker
	toolFinal             *toolCallAssembler
	promptCacheSource     promptcache.ObservationSource
	promptCacheController promptcache.Controller
	finalStreamObs        *extensions.FinalStreamObservationSession
}

func newAttemptSession(in attemptSessionInput) *attemptSession {
	return &attemptSession{
		inner:     in.inner,
		bleg:      in.bleg,
		cand:      in.cand,
		authority: in.authority,
		terminal:  newStreamTerminal(sdkterminal.ScopeAttempt),

		accounting:            in.accounting,
		toolFinal:             in.toolFinal,
		promptCacheSource:     in.promptCacheSource,
		promptCacheController: in.promptCacheController,
		finalStreamObs:        in.finalStreamObs,
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
	mu                sync.Mutex
	current           *attemptSession
	publicationClosed bool
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

// closePublicationAndSnapshot closes the replacement publication window and
// returns the attempt that was current at that boundary. Replacement Open may
// continue outside this lock, but swapIfOpen rejects its result after this point.
func (s *attemptSlot) closePublicationAndSnapshot() *attemptSession {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.publicationClosed = true
	current := s.current
	s.mu.Unlock()
	return current
}

// swapIfOpen publishes a complete replacement only while the request slot is
// still live. It returns the detached prior attempt for callers that need to
// retain its ownership while performing effects outside the slot lock.
func (s *attemptSlot) swapIfOpen(next *attemptSession) (old *attemptSession, published bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.publicationClosed {
		return s.current, false
	}
	old, s.current = s.current, next
	return old, true
}
