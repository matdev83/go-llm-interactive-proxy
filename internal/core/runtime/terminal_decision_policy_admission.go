package runtime

import (
	"context"
	"strconv"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminaldecisionpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

const terminalDecisionFeatureID = "terminal-decision"

// snapshotTerminalDecisionPolicy resolves the process policy exactly once at
// request admission. The returned values are copied into recvTurnFacts, so a
// later policy write cannot rebind an admitted request or its continuations.
func (e *Executor) snapshotTerminalDecisionPolicy(ctx context.Context, ibt *identityBoundTurn) (terminaldecision.PolicySnapshot, bool, error) {
	policy := terminaldecision.PolicySnapshot{Revision: "0"}
	if e == nil || e.RuntimeSnapshot == nil || e.RuntimeSnapshot.TerminalDecisionProvider() == nil {
		return policy, false, nil
	}
	if ibt == nil || !ibt.secureTurnOK {
		return policy, true, nil
	}

	// Minimal executors used by core tests may compose a provider without the
	// process root. Preserve their generation-default behavior; production
	// composition always supplies the process-owned store.
	if e.TerminalDecisionPolicy == nil {
		return policy, true, nil
	}

	key := terminaldecisionpolicy.Key{
		SecureSessionIncarnation: string(ibt.secureTurn.SessionID),
		ALegID:                   ibt.aLeg.ALegID,
		FeatureID:                terminalDecisionFeatureID,
	}
	authority := terminaldecisionpolicy.Authority{
		SecureSessionIncarnation: key.SecureSessionIncarnation,
		ALegID:                   key.ALegID,
		Authorized:               true,
	}
	snapshot, err := e.TerminalDecisionPolicy.Snapshot(ctx, authority, key, true)
	if err != nil {
		return policy, false, err
	}
	policy.Revision = strconv.FormatUint(snapshot.Revision, 10)
	return policy, snapshot.EffectiveEnabled, nil
}
