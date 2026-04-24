package execctx

import (
	"context"
	"maps"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type ctxKey int

const keyViews ctxKey = iota + 6200 // offset avoids collision with diag keys

// Views bundles typed snapshots for one request (design §2, R3).
type Views struct {
	Principal   execview.PrincipalView
	Session     session.SessionView
	Attempt     execview.AttemptView
	Workspace   workspace.WorkspaceView
	Annotations map[string]string
}

// WithViews returns a child context carrying v. String maps are copied so later mutation
// of the caller's maps does not affect stored snapshots.
// A nil parent is treated as [context.TODO] so the result is always non-nil.
func WithViews(ctx context.Context, v Views) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	v = copyViews(v)
	return context.WithValue(ctx, keyViews, v)
}

// FromContext returns the views attached with [WithViews], if any.
func FromContext(ctx context.Context) (Views, bool) {
	if ctx == nil {
		return Views{}, false
	}
	raw := ctx.Value(keyViews)
	if raw == nil {
		return Views{}, false
	}
	v, ok := raw.(Views)
	return v, ok
}

func copyViews(v Views) Views {
	if len(v.Principal.Claims) > 0 {
		v.Principal.Claims = maps.Clone(v.Principal.Claims)
	}
	if len(v.Principal.Roles) > 0 {
		v.Principal.Roles = append([]string(nil), v.Principal.Roles...)
	}
	if len(v.Session.Labels) > 0 {
		v.Session.Labels = maps.Clone(v.Session.Labels)
	}
	if len(v.Workspace.Labels) > 0 {
		v.Workspace.Labels = maps.Clone(v.Workspace.Labels)
	}
	if len(v.Workspace.Markers) > 0 {
		v.Workspace.Markers = append([]string(nil), v.Workspace.Markers...)
	}
	if len(v.Annotations) > 0 {
		v.Annotations = maps.Clone(v.Annotations)
	}
	return v
}
