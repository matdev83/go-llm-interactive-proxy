package continuation

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

// MaterializeCall resolves proxy-owned continuation state into a fresh
// item-authority call. Client continuation values are consumed by the store;
// the core-owned A-leg identity remains available to continuity stages.
func MaterializeCall(ctx context.Context, input lipcont.MaterializeInput, base lipapi.Call) (lipapi.Call, lipcont.MaterializedTrajectory, error) {
	trajectory, err := lipcont.Materialize(ctx, input)
	if err != nil {
		return lipapi.Call{}, lipcont.MaterializedTrajectory{}, err
	}
	out := lipapi.CloneCall(base)
	out.Items = lipcont.CloneItems(trajectory.Items)
	out.Messages = nil
	out.Instructions = nil
	out.Session.ClientSessionID = ""
	out.Session.ContinuityKey = ""
	out.Session.AuthoritativeSessionID = ""
	out.Session.ResumeToken = ""
	return out, trajectory, nil
}
