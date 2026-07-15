package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type meteringHolderKey struct{}

func withMeteringHolder(ctx context.Context, h *checkpoint.RequestHolder) context.Context {
	if h == nil {
		return ctx
	}
	return context.WithValue(ctx, meteringHolderKey{}, h)
}

func meteringHolderFrom(ctx context.Context) *checkpoint.RequestHolder {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(meteringHolderKey{}).(*checkpoint.RequestHolder)
	return h
}

// captureFrontendIngressBeforeSubmit stores one immutable FE-ingress checkpoint
// before submit hooks mutate the working call (requirement 2.1).
func captureFrontendIngressBeforeSubmit(
	ctx context.Context,
	work lipapi.Call,
	reqScope scope.PrincipalScopeView,
	now time.Time,
) (context.Context, *checkpoint.RequestHolder, error) {
	holder := meteringHolderFrom(ctx)
	if holder == nil {
		holder = &checkpoint.RequestHolder{}
		ctx = withMeteringHolder(ctx, holder)
	}
	id := strings.TrimSpace(work.ID)
	if id == "" {
		return ctx, holder, fmt.Errorf("executor: metering frontend ingress requires call id")
	}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         work,
		Scope:        reqScope,
		CheckpointID: "fe-ingress:" + id,
		StreamID:     "fe-ingress:" + id,
		Perspective:  metering.PerspectiveCustomer,
		Now:          now,
	})
	if err != nil {
		return ctx, holder, fmt.Errorf("executor: metering frontend ingress: %w", err)
	}
	return ctx, holder, nil
}

// appendMeteringFact appends a fact when a Recorder is configured; nil is a no-op.
func (e *Executor) appendMeteringFact(ctx context.Context, fact metering.Fact) error {
	if e == nil || e.MeteringRecorder == nil {
		return nil
	}
	return e.MeteringRecorder.Append(ctx, fact)
}

// enrichFrontendIngressQuantities deferred-counts the immutable FE call via the
// existing AdminCountService (tiktoken/provider path) and merges input_token
// without replacing a Present output_token bound (requirements 2.1, 4.1, 7.2).
func (e *Executor) enrichFrontendIngressQuantities(ctx context.Context) error {
	if e == nil || e.AdminCountService == nil {
		return nil
	}
	holder := meteringHolderFrom(ctx)
	if holder == nil || holder.FrontendIngress == nil {
		return nil
	}
	if _, ok := checkpoint.QuantityComponentValue(holder.FrontendIngress.Public.Quantities, metering.ComponentInputToken); ok {
		return nil
	}
	call := holder.FrontendIngress.Call
	count, err := e.AdminCountService.CountCall(ctx, accountingapp.CountCallInput{
		CallID: strings.TrimSpace(call.ID),
		Call:   call,
	})
	if err != nil {
		return err
	}
	holder.MergeFrontendIngressQuantities(countedInputQuantities(count))
	return nil
}

// countedInputQuantities maps a CountResult to deferred ingress additions.
// Output bounds are omitted so MergeQuantities keeps QuantitiesFromCall max-output.
func countedInputQuantities(count accountingapp.CountResult) []metering.Quantity {
	out := []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     int64(count.InputTokens),
		Present:   true,
	}}
	if count.CacheReadTokens > 0 {
		out = append(out, metering.Quantity{
			Component: metering.ComponentCacheReadInputToken,
			Unit:      metering.UnitToken,
			Value:     int64(count.CacheReadTokens),
			Present:   true,
		})
	}
	if count.CacheWriteTokens > 0 {
		out = append(out, metering.Quantity{
			Component: metering.ComponentCacheWriteInputToken,
			Unit:      metering.UnitToken,
			Value:     int64(count.CacheWriteTokens),
			Present:   true,
		})
	}
	return out
}

// enrichBackendIngressQuantities merges deferred operator counts into a stored
// BE snapshot without replacing conservative output bounds (reqs 2.2, 5.1).
func (e *Executor) enrichBackendIngressQuantities(holder *checkpoint.RequestHolder, attemptID string, count accountingapp.CountResult) {
	if holder == nil {
		return
	}
	holder.MergeBackendIngressQuantities(attemptID, countedInputQuantities(count))
}
