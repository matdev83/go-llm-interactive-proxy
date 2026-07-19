package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
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

// trustedFrontendIngressScope prefers the immutable FE-ingress trusted scope over
// later untrusted request metadata (requirement 5.6 / task 3.3).
func trustedFrontendIngressScope(ctx context.Context, fallback scope.PrincipalScopeView) scope.PrincipalScopeView {
	if holder := meteringHolderFrom(ctx); holder != nil && holder.FrontendIngress != nil {
		return holder.FrontendIngress.Public.Scope.Clone()
	}
	return fallback
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
	frontendID := ""
	if fe, ok := execview.FrontendIDFromContext(ctx); ok {
		frontendID = fe
	}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         work,
		Scope:        reqScope,
		FrontendID:   frontendID,
		CheckpointID: "customer-request:" + id,
		StreamID:     "customer-request:" + id,
		TraceID:      id,
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

// persistFrontendIngressFact appends the customer FE-ingress journal fact when a
// MeteringRecorder is configured and binds its FactID for rating/admission.
// Append failure is returned so strict economic paths fail closed (task 3.3).
// FactID/SourceID/Sequence are deterministic from the logical request so retry
// and process restart SameFactReplay without double-count (D6).
func (e *Executor) persistFrontendIngressFact(ctx context.Context, holder *checkpoint.RequestHolder) (string, error) {
	if e == nil || holder == nil {
		return "", nil
	}
	fe := holder.FrontendIngress
	if fe == nil {
		return "", nil
	}
	if id := strings.TrimSpace(holder.FrontendIngressFactID()); id != "" {
		return id, nil
	}
	if e.MeteringRecorder == nil {
		return "", nil
	}
	reqID := strings.TrimSpace(fe.Public.Correlation.RequestID)
	if reqID == "" {
		reqID = strings.TrimSpace(fe.Call.ID)
	}
	factID, sourceID, seq := checkpoint.FrontendIngressIdentity(reqID)
	holder.ReserveSequenceFloor(seq)
	fact, err := checkpoint.FactFromFrontendIngress(checkpoint.IngressFactInput{
		Checkpoint:      fe.Public,
		FactID:          factID,
		Sequence:        seq,
		SourceID:        sourceID,
		IdentityVersion: metering.IdentityVersionV1,
		Quantities:      append([]metering.Quantity(nil), fe.Public.Quantities...),
		Now:             e.now(),
	})
	if err != nil {
		return "", err
	}
	if err := e.appendMeteringFact(ctx, fact); err != nil {
		return "", err
	}
	holder.BindFrontendIngressFactID(factID)
	return factID, nil
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

// enrichBackendIngressQuantitiesWithDecision merges counted inputs and the
// conservative output assumption from the final preflight decision.
func (e *Executor) enrichBackendIngressQuantitiesWithDecision(
	holder *checkpoint.RequestHolder,
	attemptID string,
	decision accountingpreflight.Decision,
) {
	if holder == nil {
		return
	}
	qs := countedInputQuantities(decision.Count)
	if out, ok := explicitOutputQuantity(decision); ok {
		qs = append(qs, out)
	}
	holder.MergeBackendIngressQuantities(attemptID, qs)
}

// persistBackendIngressFact appends a real BE-ingress journal fact when a
// MeteringRecorder is configured and binds its FactID for rating/admission.
// FactID/SourceID/Sequence are deterministic per attempt so failover streams
// stay distinct and Append-fail/restart SameFactReplay without double-count.
func (e *Executor) persistBackendIngressFact(ctx context.Context, holder *checkpoint.RequestHolder, attemptID string) (string, error) {
	if e == nil || holder == nil {
		return "", nil
	}
	be := holder.BackendIngressFor(attemptID)
	if be == nil {
		return "", nil
	}
	if e.MeteringRecorder == nil {
		return "", nil
	}
	if id := strings.TrimSpace(holder.BackendIngressFactID(attemptID)); id != "" {
		return id, nil
	}
	factID, sourceID, seq := checkpoint.BackendIngressIdentity(attemptID)
	holder.ReserveSequenceFloor(seq)
	fact, err := checkpoint.FactFromIngress(checkpoint.IngressFactInput{
		Checkpoint:      be.Public,
		FactID:          factID,
		Sequence:        seq,
		SourceID:        sourceID,
		IdentityVersion: metering.IdentityVersionV1,
		Quantities:      append([]metering.Quantity(nil), be.Public.Quantities...),
		Now:             e.now(),
	})
	if err != nil {
		return "", err
	}
	if err := e.appendMeteringFact(ctx, fact); err != nil {
		return "", err
	}
	holder.BindBackendIngressFactID(attemptID, factID)
	return factID, nil
}
