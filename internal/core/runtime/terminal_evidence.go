package runtime

import (
	"slices"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreterm "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type requestTerminalFacts struct {
	call                         lipapi.Call
	traceID                      string
	aLegID                       string
	billingCallID                billing.BillingCallID
	billingState                 *billingCallState
	accountID                    string
	sessionID                    string
	pricing                      billing.VersionRef
	chargePolicy                 billing.VersionRef
	identityStamped              bool
	requestAuth                  *requestAuthorityState
	secureTurn                   execctx.SecureSessionTurn
	secureTurnOK                 bool
	replacementBlocked           bool
	routePrefs                   []string
	recvViews                    execctx.Views
	metering                     *checkpoint.RequestHolder
	conversationSnapshot         conversationview.Snapshot
	conversationProvenance       []conversationview.OverlayProvenance
	conversationFilteredBaseline lipapi.Call
}

// attemptTerminalEvidence is a value snapshot of the current B-leg identity.
// It deliberately carries no attempt owner or authority pointer.
type attemptTerminalEvidence struct {
	bleg       b2bua.BLegRecord
	candidate  routing.AttemptCandidate
	startedAt  time.Time
	accounting attemptAccountingSnapshot
}

// responseTerminalSnapshot is the terminal-facing response evidence boundary.
// Mutable response state is copied before terminal effects begin.
type responseTerminalSnapshot struct {
	accumulator   coreterm.AccumulatorSnapshot
	operatorUsage lipapi.Event
	billingEv     lipapi.Event
	usageEv       lipapi.Event
	seenEvents    []lipapi.Event
	releasedText  string
}

type responseRequestEvidence struct {
	traceID      string
	aLegID       string
	sessionID    string
	secureTurn   execctx.SecureSessionTurn
	secureTurnOK bool
}

func (f recvTurnFacts) terminalFacts() requestTerminalFacts {
	return requestTerminalFacts{
		call:                         lipapi.CloneCall(f.baseline),
		traceID:                      f.traceID,
		aLegID:                       f.aLegID,
		billingCallID:                f.billingCallID,
		billingState:                 f.billingCallState,
		accountID:                    f.billingAccountID,
		sessionID:                    f.baseline.Session.AuthoritativeSessionID,
		pricing:                      f.billingCustomerPricing,
		chargePolicy:                 f.billingChargePolicy,
		identityStamped:              f.billingIdentityStamped,
		requestAuth:                  f.requestAuth,
		secureTurn:                   f.secureTurn,
		secureTurnOK:                 f.secureTurnOK,
		routePrefs:                   slices.Clone(f.routePrefs),
		recvViews:                    f.recvViews,
		metering:                     f.metering,
		conversationSnapshot:         cloneSnapshot(f.conversationSnapshot),
		conversationProvenance:       slices.Clone(f.conversationProvenance),
		conversationFilteredBaseline: lipapi.CloneCall(f.conversationFilteredBaseline),
	}
}

func cloneSnapshot(s conversationview.Snapshot) conversationview.Snapshot {
	out := s
	if s.NeverBackend != nil {
		out.NeverBackend = slices.Clone(s.NeverBackend)
	}
	if s.Steering != nil {
		out.Steering = slices.Clone(s.Steering)
		// Deep copy anchors
		for i := range out.Steering {
			if out.Steering[i].Placement.Anchor != nil {
				cp := *out.Steering[i].Placement.Anchor
				out.Steering[i].Placement.Anchor = &cp
			}
		}
	}
	return out
}

func (f requestTerminalFacts) responseEvidence() responseRequestEvidence {
	return responseRequestEvidence{
		traceID:      f.traceID,
		aLegID:       f.aLegID,
		sessionID:    f.call.Session.AuthoritativeSessionID,
		secureTurn:   f.secureTurn,
		secureTurnOK: f.secureTurnOK,
	}
}

func (f recvTurnFacts) responseEvidence() responseRequestEvidence {
	return responseRequestEvidence{
		traceID:      f.traceID,
		aLegID:       f.aLegID,
		sessionID:    f.baseline.Session.AuthoritativeSessionID,
		secureTurn:   f.secureTurn,
		secureTurnOK: f.secureTurnOK,
	}
}

func (a *attemptSession) terminalEvidence() attemptTerminalEvidence {
	if a == nil {
		return attemptTerminalEvidence{}
	}
	return attemptTerminalEvidence{bleg: a.bleg, candidate: a.cand, startedAt: a.accounting.requestStartedAt, accounting: a.accounting.snapshot()}
}

func (p *responsePipeline) terminalEvidenceSnapshot() responseTerminalSnapshot {
	if p == nil {
		return responseTerminalSnapshot{}
	}
	return responseTerminalSnapshot{
		accumulator:   p.accumulatorSnapshot(),
		operatorUsage: p.operatorUsageForFinalize(),
		billingEv:     p.billingEvidenceFallback(),
		usageEv:       p.usageEvidenceOrEmpty(),
		seenEvents:    p.seenEventsCopy(),
		releasedText:  p.releasedOutputText(),
	}
}
