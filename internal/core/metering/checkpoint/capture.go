package checkpoint

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// Snapshot is an in-memory metering checkpoint: public journal-safe fields plus
// a sanitized Call clone used for recount/rerate. The Call body is never written
// to the metering journal by default (requirement 2.7).
type Snapshot struct {
	Public metering.Checkpoint
	Call   lipapi.Call
}

// BindScope updates Public.Scope without mutating the immutable Call clone.
func (s *Snapshot) BindScope(sc scope.PrincipalScopeView) {
	if s == nil {
		return
	}
	s.Public.Scope = sc.Clone()
}

// FrontendIngressInput captures a logical-request frontend-ingress checkpoint.
type FrontendIngressInput struct {
	Call         lipapi.Call
	Scope        scope.PrincipalScopeView
	FrontendID   string
	CheckpointID string
	StreamID     string
	Perspective  metering.EconomicPerspective
	Now          time.Time
}

// CaptureFrontendIngress clones the call before submit mutation, strips resume
// secrets, and builds a public Checkpoint (requirements 2.1, 2.5–2.8).
// It does not create usage-authority reservations.
func CaptureFrontendIngress(in FrontendIngressInput) (Snapshot, error) {
	id := strings.TrimSpace(in.CheckpointID)
	if id == "" {
		return Snapshot{}, fmt.Errorf("metering/checkpoint: checkpoint_id required")
	}
	streamID := strings.TrimSpace(in.StreamID)
	if streamID == "" {
		streamID = "fe-ingress:" + strings.TrimSpace(in.Call.ID)
	}
	if streamID == "fe-ingress:" {
		return Snapshot{}, fmt.Errorf("metering/checkpoint: stream_id or call.id required")
	}
	perspective := in.Perspective
	if perspective == "" {
		perspective = metering.PerspectiveCustomer
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cloned := SanitizeCall(lipapi.CloneCall(in.Call))
	corr := metering.Correlation{
		RequestID: strings.TrimSpace(cloned.ID),
		ALegID:    strings.TrimSpace(cloned.Session.ALegID),
		SessionID: cloned.Session.CorrelationID(),
	}
	pub := metering.Checkpoint{
		CheckpointID: id,
		StreamID:     streamID,
		Boundary:     metering.BoundaryFrontendIngress,
		Lifecycle:    metering.LifecycleLogicalRequest,
		Perspective:  perspective,
		Correlation:  corr,
		Scope:        in.Scope.Clone(),
		FrontendID:   strings.TrimSpace(in.FrontendID),
		Presence:     metering.PresenceUnknown,
		Source:       metering.SourceObserved,
		Authority:    metering.AuthorityEstimated,
		CapturedAt:   now,
	}
	if err := pub.Validate(); err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Public: pub, Call: cloned}
	snap.DeriveAndApplyIngressQuantities()
	return snap, nil
}

// RequestHolder retains the single frontend-ingress snapshot for one logical request
// and per-attempt backend-ingress freezes. Methods are safe for concurrent use by
// parallel racing attempts that store distinct AttemptID keys.
type RequestHolder struct {
	mu              sync.Mutex
	FrontendIngress *Snapshot
	BackendIngress  map[string]*Snapshot // keyed by AttemptID
	nextSeq         int64
}

// NextSequence returns a monotonically increasing fact sequence for this request.
func (h *RequestHolder) NextSequence() int64 {
	if h == nil {
		return 1
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextSeq++
	return h.nextSeq
}

// CaptureOrReuseFrontendIngress returns the existing FE ingress snapshot when set,
// otherwise captures and stores a new one (requirement 2.8).
func (h *RequestHolder) CaptureOrReuseFrontendIngress(in FrontendIngressInput) (Snapshot, error) {
	if h == nil {
		return CaptureFrontendIngress(in)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.FrontendIngress != nil {
		return *h.FrontendIngress, nil
	}
	snap, err := CaptureFrontendIngress(in)
	if err != nil {
		return Snapshot{}, err
	}
	cp := snap
	h.FrontendIngress = &cp
	return snap, nil
}

// BackendIngressInput captures a backend-attempt freeze immediately before Open.
type BackendIngressInput struct {
	Call         lipapi.Call
	Scope        scope.PrincipalScopeView
	AttemptID    string
	BLegID       string
	ALegID       string
	BackendID    string
	Model        string
	CheckpointID string
	StreamID     string
	FEStreamID   string // logical-request FE ingress stream for correlation
	Perspective  metering.EconomicPerspective
	Now          time.Time
}

// CaptureBackendIngress freezes the final provider-neutral attempt call
// (requirements 2.2, 5.1). Callers must AssertNotWidened before Open if the
// working call may still mutate.
func CaptureBackendIngress(in BackendIngressInput) (Snapshot, error) {
	id := strings.TrimSpace(in.CheckpointID)
	if id == "" {
		return Snapshot{}, fmt.Errorf("metering/checkpoint: checkpoint_id required")
	}
	attemptID := strings.TrimSpace(in.AttemptID)
	bLegID := strings.TrimSpace(in.BLegID)
	if attemptID == "" || bLegID == "" {
		return Snapshot{}, fmt.Errorf("metering/checkpoint: attempt_id and b_leg_id required")
	}
	streamID := strings.TrimSpace(in.StreamID)
	if streamID == "" {
		streamID = "be-ingress:" + attemptID
	}
	perspective := in.Perspective
	if perspective == "" {
		perspective = metering.PerspectiveOperator
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cloned := SanitizeCall(lipapi.CloneCall(in.Call))
	aLeg := strings.TrimSpace(in.ALegID)
	if aLeg == "" {
		aLeg = strings.TrimSpace(cloned.Session.ALegID)
	}
	pub := metering.Checkpoint{
		CheckpointID: id,
		StreamID:     streamID,
		Boundary:     metering.BoundaryBackendIngress,
		Lifecycle:    metering.LifecycleBackendAttempt,
		Perspective:  perspective,
		Correlation: metering.Correlation{
			RequestID: strings.TrimSpace(cloned.ID),
			ALegID:    aLeg,
			BLegID:    bLegID,
			AttemptID: attemptID,
			SessionID: cloned.Session.CorrelationID(),
			TraceID:   strings.TrimSpace(in.FEStreamID),
		},
		Scope:      in.Scope.Clone(),
		BackendID:  strings.TrimSpace(in.BackendID),
		Model:      strings.TrimSpace(in.Model),
		Presence:   metering.PresenceUnknown,
		Source:     metering.SourceObserved,
		Authority:  metering.AuthorityEstimated,
		CapturedAt: now,
	}
	if err := pub.Validate(); err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Public: pub, Call: cloned}
	snap.DeriveAndApplyIngressQuantities()
	return snap, nil
}

// StoreBackendIngress captures and retains a backend-ingress snapshot for an attempt.
func (h *RequestHolder) StoreBackendIngress(in BackendIngressInput) (Snapshot, error) {
	snap, err := CaptureBackendIngress(in)
	if err != nil {
		return Snapshot{}, err
	}
	if h == nil {
		return snap, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.BackendIngress == nil {
		h.BackendIngress = make(map[string]*Snapshot)
	}
	cp := snap
	h.BackendIngress[strings.TrimSpace(in.AttemptID)] = &cp
	return snap, nil
}

// BackendIngressFor returns the frozen snapshot for an attempt, if any.
func (h *RequestHolder) BackendIngressFor(attemptID string) *Snapshot {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.BackendIngress == nil {
		return nil
	}
	return h.BackendIngress[strings.TrimSpace(attemptID)]
}

// MergeFrontendIngressQuantities merges deferred counts into the FE snapshot
// without changing CheckpointID or the frozen Call (design deferred counting).
func (h *RequestHolder) MergeFrontendIngressQuantities(additions []metering.Quantity) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.FrontendIngress == nil {
		return
	}
	h.FrontendIngress.MergeQuantities(additions)
}

// MergeBackendIngressQuantities merges deferred counts into a stored BE snapshot.
func (h *RequestHolder) MergeBackendIngressQuantities(attemptID string, additions []metering.Quantity) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.BackendIngress == nil {
		return false
	}
	snap := h.BackendIngress[strings.TrimSpace(attemptID)]
	if snap == nil {
		return false
	}
	snap.MergeQuantities(additions)
	return true
}
