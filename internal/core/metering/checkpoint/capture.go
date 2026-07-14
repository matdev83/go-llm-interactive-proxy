package checkpoint

import (
	"fmt"
	"strings"
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
	return Snapshot{Public: pub, Call: cloned}, nil
}

// RequestHolder retains the single frontend-ingress snapshot for one logical request.
type RequestHolder struct {
	FrontendIngress *Snapshot
}

// CaptureOrReuseFrontendIngress returns the existing FE ingress snapshot when set,
// otherwise captures and stores a new one (requirement 2.8).
func (h *RequestHolder) CaptureOrReuseFrontendIngress(in FrontendIngressInput) (Snapshot, error) {
	if h == nil {
		return CaptureFrontendIngress(in)
	}
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
