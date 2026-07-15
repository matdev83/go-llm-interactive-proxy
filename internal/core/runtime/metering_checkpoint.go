package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
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
