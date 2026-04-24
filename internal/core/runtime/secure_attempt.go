package runtime

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func buildAttemptTrace(
	st execctx.SecureSessionTurn,
	aLegID string,
	bleg b2bua.BLegRecord,
	cand routing.AttemptCandidate,
	openCall lipapi.Call,
	startedAt time.Time,
) domain.AttemptTrace {
	reqModel := strings.TrimSpace(openCall.Route.Selector)
	if reqModel == "" {
		reqModel = strings.TrimSpace(cand.Primary.Model)
	}
	settings := domain.AttemptSettings{
		ReasoningEffort: strings.TrimSpace(openCall.Options.ReasoningEffort),
		Streaming:       true,
	}
	if openCall.Options.Temperature != nil {
		t := *openCall.Options.Temperature
		settings.Temperature = &t
	}
	if openCall.Options.MaxOutputTokens != nil {
		m := *openCall.Options.MaxOutputTokens
		settings.MaxTokens = &m
	}
	return domain.AttemptTrace{
		SessionID:       st.SessionID,
		TurnID:          st.TurnID,
		ALegID:          strings.TrimSpace(aLegID),
		BLegID:          strings.TrimSpace(bleg.BLegID),
		AttemptSeq:      bleg.Seq,
		RequestedModel:  reqModel,
		RequestedAlias:  "",
		ResolvedBackend: strings.TrimSpace(cand.Primary.Backend),
		ResolvedModel:   strings.TrimSpace(cand.Primary.Model),
		RouteSource:     "routing_candidate",
		RouteReason:     strings.TrimSpace(cand.Key),
		Settings:        settings,
		StartedAt:       startedAt,
	}
}

func secureAttemptOutcome(
	st execctx.SecureSessionTurn,
	bleg b2bua.BLegRecord,
	p recordAttemptParams,
	endedAt time.Time,
) domain.AttemptOutcome {
	out := domain.AttemptOutcome{
		SessionID:   st.SessionID,
		TurnID:      st.TurnID,
		BLegID:      strings.TrimSpace(bleg.BLegID),
		EndedAt:     endedAt,
		DebugReason: strings.TrimSpace(p.Reason),
	}
	switch p.Outcome {
	case lipapi.AttemptSuccess:
		out.Success = true
		out.SurfaceState = domain.SurfaceSurfaced
	case lipapi.AttemptSwallowedFailure:
		out.Success = false
		out.SurfaceState = domain.SurfaceSwallowed
	case lipapi.AttemptSurfacedFailure:
		out.Success = false
		out.SurfaceState = domain.SurfaceSurfaced
	case lipapi.AttemptCancelled:
		out.Success = false
		out.SurfaceState = domain.SurfaceFailed
		if strings.TrimSpace(out.DebugReason) == "" {
			out.DebugReason = "cancelled"
		}
	default:
		out.Success = false
		out.SurfaceState = domain.SurfaceFailed
	}
	applyErrDetailToOutcome(&out, p.DetailErr)
	return out
}

func applyErrDetailToOutcome(out *domain.AttemptOutcome, err error) {
	if err == nil || out == nil {
		return
	}
	var uf *lipapi.UpstreamFailure
	if errors.As(err, &uf) {
		if strings.TrimSpace(uf.CandidateKey) != "" {
			out.ErrorCode = strings.TrimSpace(uf.CandidateKey)
		} else {
			out.ErrorCode = "upstream_failure"
		}
		if uf.Phase == lipapi.PhasePostOutput {
			out.ProviderStatus = "post_output"
		} else {
			out.ProviderStatus = "pre_output"
		}
	}
	if errors.Is(err, context.Canceled) {
		out.TimeoutClass = "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		out.TimeoutClass = "deadline"
	}
}
