package runtime

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func (e *Executor) prepareSubmitAndALeg(ctx context.Context, bus *hooks.Bus, call *lipapi.Call) (traceID string, baseline lipapi.Call, aLeg b2bua.ALegRecord, outCtx context.Context, err error) {
	if e == nil || e.SecureSession == nil {
		return "", lipapi.Call{}, b2bua.ALegRecord{}, ctx, fmt.Errorf("executor: secure session manager is required")
	}
	return e.prepareSubmitAndALegSecure(ctx, bus, call)
}
