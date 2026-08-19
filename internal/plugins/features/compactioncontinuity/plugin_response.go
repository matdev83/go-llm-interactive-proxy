package compactioncontinuity

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
)

// BeforeResponseRelease consumes only an existing pending result. Without a
// verified plaintext augmentation capability, it records the capsule for the
// next eligible request and leaves the canonical event byte-for-byte intact.
func (p *Plugin) BeforeResponseRelease(ctx context.Context, _ *lipapi.Event, preview compaction.ResponsePreview, meta compaction.PreservationMeta, services compaction.Services) (err error) {
	defer func() {
		if recover() != nil {
			err = nil
		}
	}()
	if p == nil || p.parent == nil || ctx == nil || preview.Kind != compaction.PreviewCompletionCandidate {
		return nil
	}
	cfg, enabled := p.effectiveConfig(ctx)
	if !enabled {
		return nil
	}
	boundary := strings.TrimSpace(preview.TransactionID)
	if boundary == "" {
		return nil
	}
	parent, err := p.parent.CaptureMeta(ctx, meta)
	if err != nil || !validParentBranch(parent) {
		return nil
	}
	state, err := p.parent.Snapshot(ctx, parent)
	if err != nil {
		return nil
	}
	prepared := state.PendingInjection != nil && state.PendingInjection.BoundaryKey == boundary
	if strings.TrimSpace(string(state.PendingJobID)) != "" && services.BackgroundAux != nil {
		state, _ = p.consumePending(ctx, parent, state, services, cfg)
	}
	if state.Revision == 0 || len(state.CapsuleJSON) == 0 {
		return nil
	}
	if prepared {
		return nil
	}
	_, _ = p.parent.SetPendingInjection(ctx, parent, InjectionTarget{BoundaryKey: boundary, CapsuleRevision: state.Revision})
	return nil
}

// RequestOpenFailed lets the coordinator's bounded TTL cleanup expire the
// non-billable preview intent while preserving any pending injection target.
func (p *Plugin) RequestOpenFailed(_ context.Context, meta compaction.PreservationMeta, _ compaction.Services) error {
	p.clearPreparedMarker(meta)
	return nil
}

// AfterResponseRelease is the only release-side watermark commit point. Core
// invokes it after detector finalization and before returning the event.
func (p *Plugin) AfterResponseRelease(ctx context.Context, ev lipapi.Event, meta compaction.PreservationMeta, _ compaction.Services) (err error) {
	defer func() {
		if recover() != nil {
			err = nil
		}
	}()
	if p == nil || p.parent == nil || ctx == nil || ev.Kind != lipapi.EventResponseFinished {
		return nil
	}
	parent, err := p.parent.CaptureMeta(ctx, meta)
	if err != nil || !validParentBranch(parent) {
		return nil
	}
	state, err := p.parent.Snapshot(ctx, parent)
	if err != nil || state.PendingInjection == nil {
		return nil
	}
	boundary := strings.TrimSpace(meta.TransactionID)
	if boundary == "" || state.PendingInjection.BoundaryKey != boundary || !p.takePreparedMarker(meta, *state.PendingInjection) {
		return nil
	}
	_, _ = p.parent.CommitReleasedInjection(ctx, parent, InjectionWatermark{BranchBinding: parent.Binding, BoundaryKey: boundary, CapsuleRevision: state.PendingInjection.CapsuleRevision})
	return nil
}

var _ compaction.RequestOpenFailedPreserver = (*Plugin)(nil)
var _ compaction.AfterResponseReleasePreserver = (*Plugin)(nil)
