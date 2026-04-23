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
			SessionID: strings.TrimSpace(call.Session.ClientSessionID),
			ALegID:    strings.TrimSpace(aLeg.ALegID),
			IsNew:     aLegIsNew(aLeg),
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
