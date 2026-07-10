// Package evidencesink projects usage-authority application outcomes into the
// policydecision observer chain and the control-plane accounting-authority
// event ledger. It is the production wiring of authorityapp.EvidenceSink.
//
// The adapter is always fail-open: every recording failure is swallowed so
// authority admission and settlement never abort because of evidence
// projection (design "Evidence Sink"; requirements 9.1, 9.3, 7.6, 13.1, 13.2).
// The authority app wraps any non-nil EvidenceSink error as ErrUnavailable and
// aborts the decision, so returning nil here keeps enforcement decoupled from
// control-plane availability.
package evidencesink

import (
	"context"

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
// ledger via best-effort recording. The event is already validated by the
// authority app's projection, so ErrUnsafeEvidence is not expected; it is
// swallowed regardless to preserve fail-open behavior. Disabled, unavailable,
// and append-failure outcomes are also swallowed so authority enforcement is
// never broken by control-plane state.
func (a *Adapter) RecordAccountingAuthority(ctx context.Context, event cp.Event) error {
	if a == nil || a.recorder == nil {
		return nil
	}
	_, _ = a.recorder.RecordBestEffort(ctx, event)
	return nil
}

var _ authorityapp.EvidenceSink = (*Adapter)(nil)
