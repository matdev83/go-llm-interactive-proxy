package execctx

import (
	"maps"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

// ViewsFromSubmit builds typed views after submit hooks and A-leg resolution (task 4).
// Principal is merged from transport auth in the executor when present; workspace is filled
// after workspace resolution (task 5.1).
// Attempt fields other than TraceID are filled when an attempt opens (later tasks).
func ViewsFromSubmit(traceID string, aLeg b2bua.ALegRecord, call lipapi.Call, hookAnnotations map[string]string) Views {
	v := Views{
		Session: session.SessionView{
			AuthoritativeSessionID: strings.TrimSpace(call.Session.AuthoritativeSessionID),
			ClientSessionHint:      strings.TrimSpace(call.Session.ClientSessionID),
			ALegID:                 strings.TrimSpace(aLeg.ALegID),
			IsNew:                  aLegIsNew(aLeg),
			ResumeEligible:         false,
		},
		Attempt: execview.AttemptView{TraceID: strings.TrimSpace(traceID)},
	}
	if len(hookAnnotations) > 0 {
		v.Annotations = maps.Clone(hookAnnotations)
	}
	return v
}

func aLegIsNew(a b2bua.ALegRecord) bool {
	if a.CreatedAt.IsZero() || a.LastSeenAt.IsZero() {
		return false
	}
	return a.CreatedAt.Equal(a.LastSeenAt)
}

// SecureSubmitViewsInput carries the parameters for [ViewsFromSecureSubmit] after
// [app.Manager.BeginTurn] and B2BUA A-leg fetch.
type SecureSubmitViewsInput struct {
	TraceID                string
	ALeg                   b2bua.ALegRecord
	Call                   lipapi.Call
	HookAnnotations        map[string]string
	AuthoritativeSessionID string
	TurnID                 string
	ResumeEligible         bool
	PolicyLabels           map[string]string
}

// ViewsFromSecureSubmit builds views after [app.Manager.BeginTurn] and B2BUA A-leg fetch.
// AuthoritativeSessionID and TurnID come from validated secure-session state; Call must not carry raw resume tokens.
func ViewsFromSecureSubmit(in SecureSubmitViewsInput) Views {
	v := ViewsFromSubmit(in.TraceID, in.ALeg, in.Call, in.HookAnnotations)
	v.Session.AuthoritativeSessionID = strings.TrimSpace(in.AuthoritativeSessionID)
	v.Session.ClientSessionHint = strings.TrimSpace(in.Call.Session.ClientSessionID)
	v.Session.ResumeEligible = in.ResumeEligible
	v.Session.TurnID = strings.TrimSpace(in.TurnID)
	if len(in.PolicyLabels) > 0 {
		if v.Session.Labels == nil {
			v.Session.Labels = maps.Clone(in.PolicyLabels)
		} else {
			maps.Copy(v.Session.Labels, in.PolicyLabels)
		}
	}
	return v
}
