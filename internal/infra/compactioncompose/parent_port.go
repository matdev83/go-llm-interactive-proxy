package compactioncompose

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	corecontinuity "github.com/matdev83/go-llm-interactive-proxy/internal/core/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	featurecontinuity "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

var ErrInvalidCompactionContinuityParentPort = errors.New("compactioncompose: invalid compaction-continuity parent port")

// CompactionContinuityParentPort is the runtimebundle-owned adapter for the
// process coordinator. The coordinator remains the only state/CAS authority;
// this adapter retains a bounded-in-purpose binding-to-key map because the
// feature deliberately exposes only opaque branch bindings after Capture.
//
// Parent identity is taken from trusted execution context and detector
// metadata. The canonical Call is intentionally not consulted for identity:
// detached calls carry a private child A-leg and must never replace the
// authoritative parent A-leg.
type CompactionContinuityParentPort struct {
	coordinator *corecontinuity.BranchCoordinator

	mu      sync.RWMutex
	keys    map[string]corecontinuity.BranchKey
	maxKeys int
}

func NewCompactionContinuityParentPort(coordinator *corecontinuity.BranchCoordinator) (*CompactionContinuityParentPort, error) {
	if coordinator == nil {
		return nil, fmt.Errorf("%w: nil branch coordinator", ErrInvalidCompactionContinuityParentPort)
	}
	return &CompactionContinuityParentPort{
		coordinator: coordinator,
		keys:        make(map[string]corecontinuity.BranchKey),
		maxKeys:     corecontinuity.DefaultMaxEntries,
	}, nil
}

// Capture derives and records the authoritative parent before any detached
// child is created. For an already-detached context, the trusted parent
// marker wins over PreservationMeta's child A-leg.
func (p *CompactionContinuityParentPort) Capture(ctx context.Context, _ lipapi.Call, meta compaction.PreservationMeta) (featurecontinuity.ParentBranch, error) {
	return p.capture(ctx, meta)
}

// CaptureMeta is the response-boundary capture path. It deliberately shares
// the same trusted identity derivation as Capture and therefore cannot bind a
// result to the private child A-leg present in response metadata.
func (p *CompactionContinuityParentPort) CaptureMeta(ctx context.Context, meta compaction.PreservationMeta) (featurecontinuity.ParentBranch, error) {
	return p.capture(ctx, meta)
}

func (p *CompactionContinuityParentPort) capture(ctx context.Context, meta compaction.PreservationMeta) (featurecontinuity.ParentBranch, error) {
	if p == nil || p.coordinator == nil {
		return featurecontinuity.ParentBranch{}, ErrInvalidCompactionContinuityParentPort
	}
	key, traceID, aLegID, err := trustedParentKey(ctx, meta)
	if err != nil {
		return featurecontinuity.ParentBranch{}, err
	}
	binding, err := p.coordinator.Capture(ctx, key)
	if err != nil {
		return featurecontinuity.ParentBranch{}, err
	}
	p.remember(binding, key)
	return featurecontinuity.ParentBranch{
		Binding: binding,
		TraceID: traceID,
		ALegID:  aLegID,
		BLegID:  strings.TrimSpace(meta.BLegID),
	}, nil
}

func (p *CompactionContinuityParentPort) remember(binding string, key corecontinuity.BranchKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.keys[binding]; !exists && p.maxKeys > 0 && len(p.keys) >= p.maxKeys {
		// BranchCoordinator owns expiry and cardinality. This defensive cap keeps
		// the opaque reverse map bounded even when old branches have expired.
		for stale := range p.keys {
			delete(p.keys, stale)
			break
		}
	}
	p.keys[binding] = key
}

func (p *CompactionContinuityParentPort) Snapshot(ctx context.Context, parent featurecontinuity.ParentBranch) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	state, found, err := p.coordinator.Snapshot(ctx, key)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	if !found {
		return featurecontinuity.ParentState{}, corecontinuity.ErrBranchNotFound
	}
	return featureParentState(state), nil
}

func (p *CompactionContinuityParentPort) CommitSource(ctx context.Context, parent featurecontinuity.ParentBranch, revision uint64, source []byte, watermark string) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	state, err := p.coordinator.CommitSource(ctx, key, revision, source, watermark)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(state), nil
}

func (p *CompactionContinuityParentPort) CommitCapsule(ctx context.Context, parent featurecontinuity.ParentBranch, revision uint64, capsule []byte, digest [32]byte, watermark string) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	state, err := p.coordinator.CommitCapsule(ctx, key, revision, capsule, digest, watermark)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(state), nil
}

func (p *CompactionContinuityParentPort) RecordPendingJob(ctx context.Context, parent featurecontinuity.ParentBranch, jobID auxiliary.JobID, revision uint64) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	state, err := p.coordinator.RecordPendingJob(ctx, key, jobID, revision)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(state), nil
}

func (p *CompactionContinuityParentPort) ValidatePendingJob(ctx context.Context, parent featurecontinuity.ParentBranch, jobID auxiliary.JobID) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	state, err := p.coordinator.ValidatePendingJob(ctx, key, jobID)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(state), nil
}

func (p *CompactionContinuityParentPort) CommitCapsuleForJob(ctx context.Context, parent featurecontinuity.ParentBranch, jobID auxiliary.JobID, resultBinding string, revision uint64, capsule []byte, digest [32]byte, watermark string) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	if strings.TrimSpace(resultBinding) != parent.Binding {
		return featurecontinuity.ParentState{}, corecontinuity.ErrBranchMismatch
	}
	state, err := p.coordinator.CommitCapsuleForJob(ctx, key, jobID, resultBinding, revision, capsule, digest, watermark)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(state), nil
}

func (p *CompactionContinuityParentPort) RecordPreviewIntent(ctx context.Context, parent featurecontinuity.ParentBranch, intent featurecontinuity.PreviewIntent) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	state, err := p.coordinator.RecordPreviewIntent(ctx, key, corecontinuity.PreviewIntent{Key: intent.Key, TargetSourceRevision: intent.TargetSourceRevision})
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(state), nil
}

func (p *CompactionContinuityParentPort) BindPreviewIntent(ctx context.Context, parent featurecontinuity.ParentBranch, intentKey, transactionID string) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	state, err := p.coordinator.BindPreviewIntent(ctx, key, intentKey, transactionID)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(state), nil
}

func (p *CompactionContinuityParentPort) SetPendingInjection(ctx context.Context, parent featurecontinuity.ParentBranch, target featurecontinuity.InjectionTarget) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	state, err := p.coordinator.SetPendingInjection(ctx, key, corecontinuity.InjectionTarget{BoundaryKey: target.BoundaryKey, CapsuleRevision: target.CapsuleRevision})
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(state), nil
}

func (p *CompactionContinuityParentPort) ValidateInjection(ctx context.Context, parent featurecontinuity.ParentBranch, target featurecontinuity.InjectionTarget) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	state, err := p.coordinator.ValidateInjection(ctx, key, corecontinuity.InjectionTarget{BoundaryKey: target.BoundaryKey, CapsuleRevision: target.CapsuleRevision})
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(state), nil
}

func (p *CompactionContinuityParentPort) CommitReleasedInjection(ctx context.Context, parent featurecontinuity.ParentBranch, watermark featurecontinuity.InjectionWatermark) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	state, err := p.coordinator.CommitReleasedInjection(ctx, key, corecontinuity.InjectionWatermark{BranchBinding: watermark.BranchBinding, BoundaryKey: watermark.BoundaryKey, CapsuleRevision: watermark.CapsuleRevision})
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(state), nil
}

func (p *CompactionContinuityParentPort) key(parent featurecontinuity.ParentBranch) (corecontinuity.BranchKey, error) {
	if p == nil || p.coordinator == nil {
		return corecontinuity.BranchKey{}, ErrInvalidCompactionContinuityParentPort
	}
	binding := strings.TrimSpace(parent.Binding)
	if binding == "" {
		return corecontinuity.BranchKey{}, corecontinuity.ErrBranchMismatch
	}
	p.mu.RLock()
	key, ok := p.keys[binding]
	p.mu.RUnlock()
	if !ok || key.Binding() != binding {
		return corecontinuity.BranchKey{}, corecontinuity.ErrBranchMismatch
	}
	return key, nil
}

func featureParentState(state corecontinuity.BranchState) featurecontinuity.ParentState {
	var pendingPreviewIntent *featurecontinuity.PreviewIntent
	if state.PendingPreviewIntent != nil {
		value := featurecontinuity.PreviewIntent{Key: state.PendingPreviewIntent.Key, TargetSourceRevision: state.PendingPreviewIntent.TargetSourceRevision}
		pendingPreviewIntent = &value
	}
	var pendingInjection *featurecontinuity.InjectionTarget
	if state.PendingInjection != nil {
		value := featurecontinuity.InjectionTarget{BoundaryKey: state.PendingInjection.BoundaryKey, CapsuleRevision: state.PendingInjection.CapsuleRevision}
		pendingInjection = &value
	}
	var lastReleasedInjection *featurecontinuity.InjectionWatermark
	if state.LastReleasedInjection != nil {
		value := featurecontinuity.InjectionWatermark{BranchBinding: state.LastReleasedInjection.BranchBinding, BoundaryKey: state.LastReleasedInjection.BoundaryKey, CapsuleRevision: state.LastReleasedInjection.CapsuleRevision}
		lastReleasedInjection = &value
	}
	return featurecontinuity.ParentState{
		Revision:                  state.Revision,
		CapsuleJSON:               append([]byte(nil), state.CapsuleJSON...),
		CapsuleDigest:             state.CapsuleDigest,
		SourceJSON:                append([]byte(nil), state.SanitizedSourceJSON...),
		SourceHighWatermark:       state.SourceHighWatermark,
		PendingJobID:              state.PendingJobID,
		PendingJobTargetRevision:  state.PendingJobTargetRevision,
		PendingJobBranchBinding:   state.PendingJobBranchBinding,
		PendingPreviewIntent:      pendingPreviewIntent,
		PendingInjection:          pendingInjection,
		LastReleasedInjection:     lastReleasedInjection,
		LastCompactionTransaction: state.LastCompactionTransaction,
	}
}

// trustedParentKey uses execctx's trusted detached marker and generation views
// first. PreservationMeta is detector-produced metadata and is used only when
// no stronger value is available; canonical call session fields are ignored.
func trustedParentKey(ctx context.Context, meta compaction.PreservationMeta) (corecontinuity.BranchKey, string, string, error) {
	sessionID := strings.TrimSpace(meta.SessionID)
	aLegID := strings.TrimSpace(meta.ALegID)
	traceID := strings.TrimSpace(meta.TraceID)
	var principalPartition string
	trustedContext := false

	if detached, ok := execctx.DetachedSessionFromContext(ctx); ok {
		trustedContext = true
		// The metadata on a detached request describes the private child. Only
		// the trusted parent marker may supply the continuity identity here.
		sessionID = strings.TrimSpace(detached.ParentSessionID)
		aLegID = strings.TrimSpace(detached.ParentALegID)
		if strings.TrimSpace(detached.ParentTraceID) != "" {
			traceID = strings.TrimSpace(detached.ParentTraceID)
		}
	} else if views, ok := execctx.FromContext(ctx); ok {
		trustedContext = true
		if sid := strings.TrimSpace(views.Session.AuthoritativeSessionID); sid != "" {
			if sessionID != "" && sessionID != sid {
				return corecontinuity.BranchKey{}, "", "", fmt.Errorf("%w: trusted session mismatch", corecontinuity.ErrBranchMismatch)
			}
			sessionID = sid
		}
		if aleg := strings.TrimSpace(views.Session.ALegID); aleg != "" {
			if aLegID != "" && aLegID != aleg {
				return corecontinuity.BranchKey{}, "", "", fmt.Errorf("%w: trusted A-leg mismatch", corecontinuity.ErrBranchMismatch)
			}
			aLegID = aleg
		}
		if strings.TrimSpace(views.Attempt.TraceID) != "" && traceID == "" {
			traceID = strings.TrimSpace(views.Attempt.TraceID)
		}
		principalPartition = trustedPrincipalPartition(views.Scope, views.Principal.ID)
	} else if trustedScope, ok := scope.ScopeFromContext(ctx); ok {
		trustedContext = true
		principalPartition = trustedPrincipalPartition(trustedScope, "")
	}
	if !trustedContext {
		return corecontinuity.BranchKey{}, "", "", fmt.Errorf("%w: trusted execution context is required", corecontinuity.ErrInvalidBranchKey)
	}

	if sessionID == "" && principalPartition == "" {
		return corecontinuity.BranchKey{}, "", "", fmt.Errorf("%w: secure session or trusted principal is required", corecontinuity.ErrInvalidBranchKey)
	}
	key, err := corecontinuity.CaptureParentBranchKey(sessionID, aLegID, principalPartition)
	if err != nil {
		return corecontinuity.BranchKey{}, "", "", err
	}
	return key, traceID, aLegID, nil
}

func trustedPrincipalPartition(v scope.PrincipalScopeView, projectedPrincipal string) string {
	if value := strings.TrimSpace(v.PrincipalID.String()); value != "" {
		return value
	}
	if value := strings.TrimSpace(projectedPrincipal); value != "" {
		return value
	}
	if value := strings.TrimSpace(v.TenantID.String()); value != "" {
		return "tenant:" + value
	}
	return ""
}

var _ featurecontinuity.ParentPort = (*CompactionContinuityParentPort)(nil)
