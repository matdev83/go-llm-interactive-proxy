package policy

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
)

func trustedViews(ctx context.Context) (execctx.Views, bool) {
	views, ok := execctx.FromContext(ctx)
	if !ok || strings.TrimSpace(views.Session.AuthoritativeSessionID) == "" {
		return execctx.Views{}, false
	}
	if mode, marked := execctx.SessionModeFromContext(ctx); marked && mode == execctx.SessionModeDetached {
		return execctx.Views{}, false
	}
	return views, true
}

func overrideFromContext(ctx context.Context) Override {
	if ctx == nil {
		return Override{}
	}
	o, _ := ctx.Value(overrideContextKey{}).(Override)
	return cloneOverride(o)
}

func mergeOverride(o *Override, labels map[string]string) {
	if o == nil || len(labels) == 0 {
		return
	}
	if o.Enabled == nil {
		o.Enabled = parseBool(labels[LabelEnabled])
	}
	if !o.RouteSet {
		if route := strings.TrimSpace(labels[LabelRoute]); route != "" {
			o.Route, o.RouteSet, o.routeApproved = route, true, true
		}
	}
	if o.Inherit == nil {
		o.Inherit = parseBool(labels[LabelInherit])
	}
	patch := CategoryPatch{}
	categorySet := false
	for _, item := range []struct {
		key string
		dst **bool
	}{{LabelPlan, &patch.Plan}, {LabelUserDecisions, &patch.UserDecisions}, {LabelConstraints, &patch.Constraints}, {LabelRationale, &patch.Rationale}, {LabelRejected, &patch.RejectedAlternatives}} {
		if value := parseBool(labels[item.key]); value != nil {
			*item.dst, categorySet = value, true
		}
	}
	if o.Preserve == nil && o.PreservePatch == nil && categorySet {
		o.PreservePatch = &patch
	}
	o.Limits = mergeLimitLabels(o.Limits, labels)
}

func mergeLimitLabels(o LimitOverride, labels map[string]string) LimitOverride {
	if o.Timeout == nil {
		o.Timeout = parseDuration(labels[LabelTimeout])
	}
	if o.MaxInputTokens == nil {
		o.MaxInputTokens = parsePositiveInt(labels[LabelMaxInputTokens])
	}
	if o.MaxOutputTokens == nil {
		o.MaxOutputTokens = parsePositiveInt(labels[LabelMaxOutputTokens])
	}
	if o.BarrierTimeout == nil {
		o.BarrierTimeout = parseDuration(labels[LabelBarrierTimeout])
	}
	if o.CapsuleMaxTokens == nil {
		o.CapsuleMaxTokens = parsePositiveInt(labels[LabelCapsuleMaxTokens])
	}
	if o.CapsuleMaxBytes == nil {
		o.CapsuleMaxBytes = parsePositiveInt(labels[LabelCapsuleMaxBytes])
	}
	if o.SourceMaxBytes == nil {
		o.SourceMaxBytes = parsePositiveInt(labels[LabelSourceMaxBytes])
	}
	if o.ResultMaxBytes == nil {
		o.ResultMaxBytes = parsePositiveInt(labels[LabelResultMaxBytes])
	}
	if o.ResultMaxCount == nil {
		o.ResultMaxCount = parsePositiveInt(labels[LabelResultMaxCount])
	}
	return o
}

func parseBool(raw string) *bool {
	v, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return nil
	}
	return &v
}

func parsePositiveInt(raw string) *int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v <= 0 {
		return nil
	}
	return &v
}

func parseDuration(raw string) *time.Duration {
	v, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || v <= 0 {
		return nil
	}
	return &v
}

func cloneOverride(in Override) Override {
	out := in
	if in.Preserve != nil {
		v := *in.Preserve
		out.Preserve = &v
	}
	if in.PreservePatch != nil {
		v := *in.PreservePatch
		if v.Plan != nil {
			x := *v.Plan
			v.Plan = &x
		}
		if v.UserDecisions != nil {
			x := *v.UserDecisions
			v.UserDecisions = &x
		}
		if v.Constraints != nil {
			x := *v.Constraints
			v.Constraints = &x
		}
		if v.Rationale != nil {
			x := *v.Rationale
			v.Rationale = &x
		}
		if v.RejectedAlternatives != nil {
			x := *v.RejectedAlternatives
			v.RejectedAlternatives = &x
		}
		out.PreservePatch = &v
	}
	cloneDuration := func(v *time.Duration) *time.Duration {
		if v == nil {
			return nil
		}
		x := *v
		return &x
	}
	cloneInt := func(v *int) *int {
		if v == nil {
			return nil
		}
		x := *v
		return &x
	}
	out.Limits.Timeout = cloneDuration(in.Limits.Timeout)
	out.Limits.MaxInputTokens = cloneInt(in.Limits.MaxInputTokens)
	out.Limits.MaxOutputTokens = cloneInt(in.Limits.MaxOutputTokens)
	out.Limits.BarrierTimeout = cloneDuration(in.Limits.BarrierTimeout)
	out.Limits.CapsuleMaxTokens = cloneInt(in.Limits.CapsuleMaxTokens)
	out.Limits.CapsuleMaxBytes = cloneInt(in.Limits.CapsuleMaxBytes)
	out.Limits.SourceMaxBytes = cloneInt(in.Limits.SourceMaxBytes)
	out.Limits.ResultMaxBytes = cloneInt(in.Limits.ResultMaxBytes)
	out.Limits.ResultMaxCount = cloneInt(in.Limits.ResultMaxCount)
	if in.Enabled != nil {
		v := *in.Enabled
		out.Enabled = &v
	}
	if in.Inherit != nil {
		v := *in.Inherit
		out.Inherit = &v
	}
	return out
}

// WithSecureTurn is a test/composition helper that carries only the existing
// trusted secure-session policy seam; it has no wire representation.
func WithSecureTurn(ctx context.Context, transcriptEnabled bool) context.Context {
	return execctx.WithSecureSessionTurn(ctx, execctx.SecureSessionTurn{SessionID: domain.SessionID("policy-session"), TurnID: domain.TurnID("policy-turn"), Policy: domain.PolicyMetadata{TranscriptEnabled: transcriptEnabled}})
}
