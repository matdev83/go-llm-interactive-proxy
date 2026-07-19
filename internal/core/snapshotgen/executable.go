package snapshotgen

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/runtimegen"
)

// ExecutableGeneration is one immutable published set of evaluator objects used
// by admission and settlement (requirements 9.1–9.9; design D10).
type ExecutableGeneration struct {
	ID          int64
	Version     string
	SourceID    string
	PublishedAt time.Time
	State       economics.SnapshotState
	Reason      string

	Usage       economics.Snapshot[economics.PolicyRulesView]
	Concurrency economics.Snapshot[economics.PolicyRulesView]
	Rating      economics.Snapshot[economics.RatingCatalogView]

	RequestRegistrations    []authority.RequestRegistration
	AttemptRegistrations    []authority.AttemptRegistration
	ConcurrencyRegistration *authority.ConcurrencyRegistration
	CustomerRaters          []economics.RaterRegistration
	OperatorRaters          []economics.RaterRegistration

	// RequestCoord/AttemptCoord are the concrete immutable evaluators for this
	// generation. New admissions must use these when present (D10).
	RequestCoord  *authoritycoord.RequestCoordinator
	AttemptCoord  *authoritycoord.AttemptCoordinator
	CustomerRater economics.Rater
	OperatorRater economics.Rater

	MaxActiveRequests int
	RatingObjectID    string

	liveRefs   atomic.Int64
	pendingMu  sync.Mutex
	pendingRef map[string]int
}

// ValidateComplete rejects metadata-only generations that lack executable objects.
func (g *ExecutableGeneration) ValidateComplete() error {
	if g == nil {
		return fmt.Errorf("snapshotgen: nil executable generation")
	}
	if strings.TrimSpace(g.Version) == "" {
		return fmt.Errorf("snapshotgen: generation version required")
	}
	if g.State == economics.SnapshotReady {
		if g.RequestCoord == nil && g.AttemptCoord == nil && g.CustomerRater == nil && g.OperatorRater == nil &&
			g.ConcurrencyRegistration == nil && len(g.RequestRegistrations) == 0 &&
			len(g.AttemptRegistrations) == 0 && len(g.CustomerRaters) == 0 && len(g.OperatorRaters) == 0 {
			return fmt.Errorf("snapshotgen: metadata-only generation rejected (D10)")
		}
		if g.MaxActiveRequests > 0 && (g.RequestCoord == nil || g.RequestCoord.Concurrency == nil) {
			return fmt.Errorf("snapshotgen: ready concurrency generation requires request coordinator concurrency")
		}
		if g.ConcurrencyRegistration != nil && g.MaxActiveRequests <= 0 {
			return fmt.Errorf("snapshotgen: ready concurrency generation requires max_active_requests")
		}
		if len(g.OperatorRaters)+len(g.CustomerRaters) > 0 && strings.TrimSpace(g.RatingObjectID) == "" {
			return fmt.Errorf("snapshotgen: ready rating generation requires rating object id")
		}
	}
	return nil
}

// EnforcementMaxActive returns the concurrency limit from the actual generation objects.
func (g *ExecutableGeneration) EnforcementMaxActive() int {
	if g == nil {
		return 0
	}
	return g.MaxActiveRequests
}

// EvidenceObjectID identifies the executable object for version evidence (9.9).
func (g *ExecutableGeneration) EvidenceObjectID() string {
	if g == nil {
		return ""
	}
	if id := strings.TrimSpace(g.RatingObjectID); id != "" {
		return id
	}
	if g.ConcurrencyRegistration != nil {
		return strings.TrimSpace(g.ConcurrencyRegistration.Descriptor.ID)
	}
	if len(g.RequestRegistrations) > 0 {
		return strings.TrimSpace(g.RequestRegistrations[0].Descriptor.ID)
	}
	return fmt.Sprintf("generation:%d:%s", g.ID, g.Version)
}

// Retain increments the live reference count (requirement 9.8).
func (g *ExecutableGeneration) Retain() {
	if g == nil {
		return
	}
	g.liveRefs.Add(1)
}

// Release decrements the live reference count.
func (g *ExecutableGeneration) Release() {
	if g == nil {
		return
	}
	for {
		cur := g.liveRefs.Load()
		if cur <= 0 {
			return
		}
		if g.liveRefs.CompareAndSwap(cur, cur-1) {
			return
		}
	}
}

// LiveRefs reports outstanding live request bindings.
func (g *ExecutableGeneration) LiveRefs() int64 {
	if g == nil {
		return 0
	}
	return g.liveRefs.Load()
}

// AddPendingProvider tracks pending terminal-work references to a provider ID.
func (g *ExecutableGeneration) AddPendingProvider(providerID string) {
	if g == nil {
		return
	}
	id := strings.TrimSpace(providerID)
	if id == "" {
		return
	}
	g.pendingMu.Lock()
	defer g.pendingMu.Unlock()
	if g.pendingRef == nil {
		g.pendingRef = make(map[string]int)
	}
	g.pendingRef[id]++
}

// ClearPendingProvider decrements a pending provider reference.
func (g *ExecutableGeneration) ClearPendingProvider(providerID string) {
	if g == nil {
		return
	}
	id := strings.TrimSpace(providerID)
	if id == "" {
		return
	}
	g.pendingMu.Lock()
	defer g.pendingMu.Unlock()
	cur := g.pendingRef[id]
	if cur <= 1 {
		delete(g.pendingRef, id)
		return
	}
	g.pendingRef[id] = cur - 1
}

// PendingProviderIDs returns provider IDs with outstanding pending work.
func (g *ExecutableGeneration) PendingProviderIDs() []string {
	if g == nil {
		return nil
	}
	g.pendingMu.Lock()
	defer g.pendingMu.Unlock()
	out := make([]string, 0, len(g.pendingRef))
	for id, n := range g.pendingRef {
		if n > 0 {
			out = append(out, id)
		}
	}
	return out
}

// CanRemoveProvider reports whether providerID may be removed from configuration.
func (g *ExecutableGeneration) CanRemoveProvider(providerID string) bool {
	if g == nil {
		return true
	}
	id := strings.TrimSpace(providerID)
	if id == "" {
		return true
	}
	if g.LiveRefs() > 0 && g.hasProvider(id) {
		return false
	}
	g.pendingMu.Lock()
	defer g.pendingMu.Unlock()
	return g.pendingRef[id] <= 0
}

func (g *ExecutableGeneration) hasProvider(id string) bool {
	if g.ConcurrencyRegistration != nil && strings.TrimSpace(g.ConcurrencyRegistration.Descriptor.ID) == id {
		return true
	}
	for _, reg := range g.RequestRegistrations {
		if strings.TrimSpace(reg.Descriptor.ID) == id {
			return true
		}
	}
	for _, reg := range g.AttemptRegistrations {
		if strings.TrimSpace(reg.Descriptor.ID) == id {
			return true
		}
	}
	return false
}

// CompileExecutable builds an ExecutableGeneration from a validated contribution.
func CompileExecutable(contrib runtimegen.GenerationContribution) (*ExecutableGeneration, error) {
	if err := contrib.Validate(); err != nil {
		return nil, err
	}
	state := contrib.State
	if state == "" {
		state = economics.SnapshotReady
	}
	reqCoord, attCoord, customerRater, operatorRater, err := buildCoordinators(contrib)
	if err != nil {
		return nil, err
	}
	gen := &ExecutableGeneration{
		Version:                 strings.TrimSpace(contrib.Version),
		SourceID:                strings.TrimSpace(contrib.SourceID),
		PublishedAt:             contrib.EffectiveAt,
		State:                   state,
		RequestRegistrations:    append([]authority.RequestRegistration(nil), contrib.RequestRegistrations...),
		AttemptRegistrations:    append([]authority.AttemptRegistration(nil), contrib.AttemptRegistrations...),
		ConcurrencyRegistration: contrib.ConcurrencyRegistration,
		CustomerRaters:          append([]economics.RaterRegistration(nil), contrib.CustomerRaters...),
		OperatorRaters:          append([]economics.RaterRegistration(nil), contrib.OperatorRaters...),
		RequestCoord:            reqCoord,
		AttemptCoord:            attCoord,
		CustomerRater:           customerRater,
		OperatorRater:           operatorRater,
		MaxActiveRequests:       contrib.MaxActiveRequests,
	}
	if gen.PublishedAt.IsZero() {
		gen.PublishedAt = time.Now().UTC()
	}
	if len(contrib.OperatorRaters) > 0 {
		gen.RatingObjectID = strings.TrimSpace(contrib.OperatorRaters[0].ID)
	} else if len(contrib.CustomerRaters) > 0 {
		gen.RatingObjectID = strings.TrimSpace(contrib.CustomerRaters[0].ID)
	}
	if err := gen.ValidateComplete(); err != nil {
		return nil, err
	}
	return gen, nil
}
