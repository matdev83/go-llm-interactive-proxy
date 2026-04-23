package execctx

import (
	"context"
	"slices"
)

type routeCandPrefKey struct{}

// WithRouteCandidatePreferences attaches advisory routing candidate key ordering hints for the
// planner (design §12). prefs is copied; duplicate keys may appear but reordering treats first wins.
func WithRouteCandidatePreferences(ctx context.Context, prefs []string) context.Context {
	if ctx == nil || len(prefs) == 0 {
		return ctx
	}
	cp := slices.Clone(prefs)
	return context.WithValue(ctx, routeCandPrefKey{}, cp)
}

// RouteCandidatePreferences returns planner preference keys from [WithRouteCandidatePreferences].
func RouteCandidatePreferences(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	raw, ok := ctx.Value(routeCandPrefKey{}).([]string)
	if !ok || len(raw) == 0 {
		return nil
	}
	return slices.Clone(raw)
}
