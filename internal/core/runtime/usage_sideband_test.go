package runtime

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

type usageSidebandStream struct {
	mu       sync.Mutex
	evidence []lipapi.Event
	event    lipapi.Event
	err      error
}

func (s *usageSidebandStream) Recv(context.Context) (lipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.event.Kind != "" {
		ev := s.event
		s.event = lipapi.Event{}
		return ev, nil
	}
	return lipapi.Event{}, s.err
}

func (s *usageSidebandStream) Close() error { return nil }

func (s *usageSidebandStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func (s *usageSidebandStream) DrainUsageEvidence() []lipapi.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]lipapi.Event(nil), s.evidence...)
	s.evidence = nil
	return out
}

type usageSidebandObserver struct {
	mu     sync.Mutex
	events []usage.Event
}

func (o *usageSidebandObserver) OnUsage(_ context.Context, ev usage.Event) error {
	o.mu.Lock()
	o.events = append(o.events, ev)
	o.mu.Unlock()
	return nil
}

func TestConsumeBackendUsageEvidenceDrainsOnceWithoutCanonicalRecv(t *testing.T) {
	stream := &usageSidebandStream{
		evidence: []lipapi.Event{{
			Kind: lipapi.EventUsageDelta, InputTokens: 13,
			Accounting: lipapi.UsageAccountingMetadata{DedupeKey: "compaction-1"},
		}},
		event: lipapi.Event{Kind: lipapi.EventResponseFinished}, err: io.EOF,
	}
	rs := &retryRecvStream{}
	rs.consumeBackendUsageEvidence(context.Background(), stream)
	rs.consumeBackendUsageEvidence(context.Background(), stream)

	if got := len(rs.seenEventsCopy()); got != 1 {
		t.Fatalf("seen events = %d, want one sideband event", got)
	}
	if ev := rs.seenEventsCopy()[0]; ev.Kind != lipapi.EventUsageDelta || ev.InputTokens != 13 {
		t.Fatalf("sideband event = %#v", ev)
	}
}

func TestUsageEvidenceDedupeKeySuppressesCanonicalReplay(t *testing.T) {
	rs := &retryRecvStream{}
	first := lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 13, Accounting: lipapi.UsageAccountingMetadata{DedupeKey: "compaction-1"}}
	if !rs.rememberUsageEvidenceOnce(first) {
		t.Fatal("first evidence was unexpectedly treated as duplicate")
	}
	if rs.rememberUsageEvidenceOnce(first) {
		t.Fatal("replayed evidence key was not deduplicated")
	}
}
