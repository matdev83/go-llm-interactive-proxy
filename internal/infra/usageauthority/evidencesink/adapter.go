// Package evidencesink projects usage-authority application outcomes into the
// policydecision observer chain and the control-plane accounting-authority
// event ledger. It is the production wiring of authorityapp.EvidenceSink.
//
// Policy-observer fan-out remains best-effort, while accounting-authority
// recording delegates to the recorder's configured policy. Required pre-work
// recorder failures are returned so protected admission cannot proceed without
// mandatory evidence.
package evidencesink

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

// Adapter projects authority outcomes to the policy observer chain and the
// control-plane recorder. Either dependency may be nil; the corresponding
// projection becomes a no-op.
type Adapter struct {
	recorder  *controlplane.RecorderService
	policyObs policydecision.Observer
}

// New constructs an Adapter. recorder may be nil (no accounting-authority
// event is appended); policyObs may be nil (no policydecision fan-out).
func New(recorder *controlplane.RecorderService, policyObs policydecision.Observer) *Adapter {
	return &Adapter{recorder: recorder, policyObs: policyObs}
}

// RecordPolicyDecision fans the authority decision to the policy observer
// chain (operator observers plus the control-plane policy observer adapter).
// It is always fail-open: observer errors are discarded so authority
// admission and settlement never abort because of evidence projection.
func (a *Adapter) RecordPolicyDecision(ctx context.Context, record policydecision.Record) error {
	if a == nil || a.policyObs == nil {
		return nil
	}
	_ = a.policyObs.OnPolicyDecision(ctx, record)
	return nil
}

// RecordAccountingAuthority appends the authority event to the control-plane
// ledger using the recorder's configured policy. Required pre-work recorder
// failures are returned through the app-level sentinel so admission can fail
// closed before protected backend work starts.
func (a *Adapter) RecordAccountingAuthority(ctx context.Context, event cp.Event) error {
	if a == nil || a.recorder == nil {
		return nil
	}
	if _, err := a.recorder.Record(ctx, event); err != nil {
		return fmt.Errorf("%w: %w", authorityapp.ErrRequiredEvidence, err)
	}
	return nil
}

var _ authorityapp.EvidenceSink = (*Adapter)(nil)
