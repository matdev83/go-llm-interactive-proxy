package b2bua

import (
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routeoverride"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func (s *MemoryStore) SeedRouteOverrideForTest(aLegID string, st routeoverride.State) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	leg, ok := s.legs[aLegID]
	if !ok {
		return false
	}
	leg.override = st
	return true
}

func (s *MemoryStore) PeekLastSeenAtForTest(aLegID string) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	leg, ok := s.legs[aLegID]
	if !ok {
		return time.Time{}, false
	}
	return leg.record.LastSeenAt, true
}

func (s *MemoryStore) CreateLegForConversationViewTest(aLegID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.legs[aLegID]; exists {
		return
	}
	now := s.nowTime()
	s.legs[aLegID] = &legState{
		record: ALegRecord{
			ALegID:        aLegID,
			ContinuityKey: aLegID,
			CreatedAt:     now,
			LastSeenAt:    now,
		},
		seqToBLeg:    make(map[int]string),
		attemptBySeq: make(map[int]lipapi.AttemptRecord),
	}
	_ = lipapi.ItemKindMessage
	// Enforce MaxLegs like CreateALeg does so eviction semantics are covered.
	s.enforceMaxLegsLocked()
}

func (s *MemoryStore) DeleteLegForConversationViewTest(aLegID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLegLocked(aLegID)
}
