package auxreq

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

// ExecutorRunner is satisfied by [*runtime.Executor] for auxiliary delegation.
type ExecutorRunner interface {
	Execute(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error)
}

// Client implements [auxiliary.Client] by delegating to the runtime executor (design §7).
type Client struct {
	exec func() ExecutorRunner
}

// NewClient wraps a lazily resolved executor pointer so [runtimebundle.Build] can bind
// the snapshot before the executor value exists.
func NewClient(exec func() ExecutorRunner) auxiliary.Client {
	if exec == nil {
		return auxiliary.DisabledClient{}
	}
	return Client{exec: exec}
}

func (c Client) Stream(ctx context.Context, req auxiliary.Request) (lipapi.EventStream, error) {
	if req.Call == nil {
		return nil, fmt.Errorf("auxreq: nil call: %w", lipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return nil, lipapi.ErrNilContext
	}
	run := c.exec()
	if run == nil {
		return nil, auxiliary.ErrNotConfigured
	}
	childCtx, ok := execctx.IncAuxiliaryDepth(ctx)
	if !ok {
		return nil, auxiliary.ErrAuxDepthExceeded
	}
	if len(req.DisablePlugins) > 0 {
		childCtx = execctx.WithSuppressedPluginIDs(childCtx, req.DisablePlugins)
	}
	work := lipapi.CloneCall(*req.Call)
	work.ID = childAuxTraceID(req.ParentTraceID)
	if work.Extensions == nil {
		work.Extensions = map[string]json.RawMessage{}
	}
	encodeLineage(&work, req)

	return run.Execute(childCtx, &work)
}

func (c Client) Collect(ctx context.Context, req auxiliary.Request) (lipapi.Collected, error) {
	s, err := c.Stream(ctx, req)
	if err != nil {
		return lipapi.Collected{}, err
	}
	return lipapi.Collect(ctx, s)
}

func childAuxTraceID(parent string) string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%s-aux-fallback", parent)
	}
	suffix := hex.EncodeToString(buf[:])
	if parent != "" {
		return parent + "-aux-" + suffix
	}
	return "aux-" + suffix
}

func encodeLineage(call *lipapi.Call, req auxiliary.Request) {
	// Small JSON blob for observability and future traffic legs; not authoritative for routing.
	type lineage struct {
		Role          string `json:"role,omitempty"`
		Visibility    string `json:"visibility,omitempty"`
		ParentTraceID string `json:"parent_trace_id,omitempty"`
		ParentALegID  string `json:"parent_a_leg_id,omitempty"`
		ParentBLegID  string `json:"parent_b_leg_id,omitempty"`
	}
	ln := lineage{
		Role:          req.Role,
		Visibility:    req.Visibility,
		ParentTraceID: req.ParentTraceID,
		ParentALegID:  req.ParentALegID,
		ParentBLegID:  req.ParentBLegID,
	}
	raw, err := json.Marshal(ln)
	if err != nil {
		return
	}
	const extKey = "lip.aux.lineage.v1"
	call.Extensions[extKey] = raw
}

var _ auxiliary.Client = Client{}
