package modelcatalog

import (
	"context"
	"math"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// EstimateBasis documents how [SizeEstimate.Input] was derived (requirement 7.7).
const (
	EstimateBasisCanonicalUTF8                  = "canonical_utf8_bytes"
	EstimateBasisCanonicalUTF8AndTools          = "canonical_utf8_bytes+tools_json_bytes"
	EstimateBasisCanonicalUTF8AndSession        = "canonical_utf8_bytes+session_bytes"
	EstimateBasisSessionContributionUnavailable = "session_contribution_unavailable"
)

// sizeEstimator is the internal estimate hook used by [EligibilityResolverImpl].
type sizeEstimator interface {
	Estimate(ctx context.Context, call lipapi.Call) SizeEstimate
}

// SizeEstimate is a deterministic, diagnostics-friendly size view (design §SizeEstimate).
type SizeEstimate struct {
	Available bool
	Units     string
	Input     int64
	Basis     string
}

type ctxKeySessionSize struct{}

// WithSessionSizeContribution attaches a proxy-known byte contribution for resumed or continuous sessions.
// When the call carries session or continuity hints, this value must be present for an available estimate.
func WithSessionSizeContribution(ctx context.Context, contributionBytes int64) context.Context {
	return context.WithValue(ctx, ctxKeySessionSize{}, contributionBytes)
}

// addSaturatingInt64 returns a+b clamped at math.MaxInt64 so size estimates never wrap negative on overflow.
func addSaturatingInt64(a, b int64) int64 {
	if a < 0 {
		a = 0
	}
	if b < 0 {
		b = 0
	}
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func sessionSizeContributionFromContext(ctx context.Context) (int64, bool) {
	if ctx == nil {
		return 0, false
	}
	v := ctx.Value(ctxKeySessionSize{})
	if v == nil {
		return 0, false
	}
	n, ok := v.(int64)
	return n, ok
}

// DefaultSizeEstimator implements conservative sizing using UTF-8 byte counts of canonical message content
// plus tool declaration JSON-ish footprint. Non-text parts use deterministic byte proxies.
type DefaultSizeEstimator struct{}

var _ sizeEstimator = DefaultSizeEstimator{}

func (DefaultSizeEstimator) Estimate(ctx context.Context, call lipapi.Call) SizeEstimate {
	body := canonicalMessageBytes(call)
	tools := toolsFootprintBytes(call.Tools)

	if needsSessionContribution(call.Session) {
		extra, ok := sessionSizeContributionFromContext(ctx)
		if !ok {
			return SizeEstimate{Available: false, Units: "bytes", Input: 0, Basis: EstimateBasisSessionContributionUnavailable}
		}
		total := addSaturatingInt64(addSaturatingInt64(body, tools), extra)
		basis := estimateBasisForSessionPath(tools > 0)
		return SizeEstimate{Available: true, Units: "bytes", Input: total, Basis: basis}
	}

	total := addSaturatingInt64(body, tools)
	if tools > 0 {
		return SizeEstimate{Available: true, Units: "bytes", Input: total, Basis: EstimateBasisCanonicalUTF8AndTools}
	}
	return SizeEstimate{Available: true, Units: "bytes", Input: total, Basis: EstimateBasisCanonicalUTF8}
}

func estimateBasisForSessionPath(hasTools bool) string {
	if hasTools {
		return EstimateBasisCanonicalUTF8AndTools + "+session_bytes"
	}
	return EstimateBasisCanonicalUTF8AndSession
}

func needsSessionContribution(s lipapi.SessionRef) bool {
	// Only resume-token turns imply additional upstream transcript bytes beyond this request's
	// canonical envelope. Continuity and authoritative session ids are correlation hints and do
	// not by themselves imply unknown size unless a caller attaches [WithSessionSizeContribution].
	//
	// TODO: Wire [WithSessionSizeContribution] from the executor when the proxy has a conservative
	// byte estimate for resumed session context (e.g. from secure-session transcript accounting).
	// Until then, resume turns report EstimateBasisSessionContributionUnavailable (Req 7.5 safe).
	if strings.TrimSpace(s.ResumeToken) != "" {
		return true
	}
	return false
}

func canonicalMessageBytes(call lipapi.Call) int64 {
	var n int64
	for _, m := range call.Instructions {
		n = addSaturatingInt64(n, messageBytes(m))
	}
	for _, m := range call.Messages {
		n = addSaturatingInt64(n, messageBytes(m))
	}
	return n
}

func messageBytes(m lipapi.Message) int64 {
	var n int64
	for _, p := range m.Parts {
		n = addSaturatingInt64(n, partBytes(p))
	}
	return n
}

func partBytes(p lipapi.Part) int64 {
	switch p.Kind {
	case lipapi.PartText:
		return int64(len(p.Text))
	case lipapi.PartJSON:
		return int64(len(p.Content))
	case lipapi.PartToolResult:
		return int64(len(p.Content)) + int64(len(p.ToolCallID)) + int64(len(p.ToolName))
	case lipapi.PartImageRef:
		return int64(len(p.ImageRef)) + int64(len(p.ImageMIME))
	case lipapi.PartFileRef:
		return int64(len(p.FileRef)) + int64(len(p.FileMIME)) + int64(len(p.FileName))
	default:
		return 0
	}
}

func toolsFootprintBytes(tools []lipapi.ToolDef) int64 {
	var n int64
	for _, t := range tools {
		n = addSaturatingInt64(n, int64(len(t.Name)))
		n = addSaturatingInt64(n, int64(len(t.Description)))
		n = addSaturatingInt64(n, int64(len(t.Parameters)))
	}
	return n
}
