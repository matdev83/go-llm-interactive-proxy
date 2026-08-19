package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	coreterm "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
)

// responsePipeline owns mutable evidence and client-event state for one logical
// response. Recv is the only writer during normal streaming; eventsMu gives
// terminal/Close a short, coherent snapshot boundary. It has no terminal or
// commitment authority.
type responsePipeline struct {
	eventsMu sync.Mutex

	customer    *customerEvidenceAccumulator
	seenEvents  []lipapi.Event
	visibleText strings.Builder

	gateBuf   []lipapi.Event
	gateDrain []lipapi.Event
	gateLive  bool

	recoverDrain []lipapi.Event

	// toolClass and committedTools live for the logical response. The active
	// attempt's assembler/finalizer remains on attemptSession and is replaced
	// with every B-leg.
	toolClass      toolEventClassificationState
	committedTools []lipapi.ToolEvent

	// compactionOpenMeta is request-side response correlation carried into the
	// final release seam. It is response evidence, not terminal authority.
	compactionOpenMeta compaction.PreservationMeta

	// recordingOutcome is the last typed secure-recording result. It lets the
	// caller apply replacement/terminal policy without a second hard-stop flag.
	recordingOutcome responseRecordingOutcome

	lastAuthorityUsage lipapi.Event
	lastCustomerUsage  lipapi.Event
	keepwarmArmOnce    sync.Once

	terminalSnapshot func() (committed, accountingFinalized bool)
	customerUsageFn  func(context.Context, string, []lipapi.Event) lipapi.Event
}

func newResponsePipeline(openMeta ...compaction.PreservationMeta) *responsePipeline {
	p := &responsePipeline{customer: newCustomerEvidenceAccumulator()}
	if len(openMeta) > 0 {
		p.compactionOpenMeta = openMeta[0]
	}
	return p
}

func (p *responsePipeline) ensure() {
	if p == nil {
		return
	}
	if p.customer == nil {
		p.customer = newCustomerEvidenceAccumulator()
	}
}

// bound reports whether the production stream hooks have been installed. The
// assembly paths bind them immediately after constructing the stream; the
// guarded fallback is only for focused tests that build retryRecvStream
// literals directly.
func (p *responsePipeline) bound() bool {
	if p == nil {
		return false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return p.terminalSnapshot != nil
}

func (p *responsePipeline) bindTerminalSnapshot(snapshot func() (bool, bool)) {
	if p == nil || snapshot == nil {
		return
	}
	p.eventsMu.Lock()
	p.terminalSnapshot = snapshot
	p.eventsMu.Unlock()
}

func (p *responsePipeline) bindCustomerUsage(fn func(context.Context, string, []lipapi.Event) lipapi.Event) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.customerUsageFn = fn
	p.eventsMu.Unlock()
}

func (p *responsePipeline) rememberInternalUsage(ev lipapi.Event) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.seenEvents = append(p.seenEvents, ev)
	p.eventsMu.Unlock()
}

func (p *responsePipeline) rememberClientEvent(ev lipapi.Event) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.ensure()
	if ev.Kind == lipapi.EventResponseFinished {
		for _, seen := range p.seenEvents {
			if seen.Kind == lipapi.EventResponseFinished {
				p.eventsMu.Unlock()
				return
			}
		}
	}
	if p.customer != nil {
		p.customer.ObserveReleased(ev)
	} else if ev.Kind == lipapi.EventTextDelta {
		p.visibleText.WriteString(ev.Delta)
	}
	if tool, ok := lipapi.ToolEventFromEvent(ev); ok {
		p.committedTools = append(p.committedTools, tool)
	}
	p.seenEvents = append(p.seenEvents, ev)
	p.eventsMu.Unlock()
}

func (p *responsePipeline) usageEventsSnapshot() []lipapi.Event {
	if p == nil {
		return nil
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return tokenAccountingUsageEvents(append([]lipapi.Event(nil), p.seenEvents...))
}

func (p *responsePipeline) seenEventsCopy() []lipapi.Event {
	if p == nil {
		return nil
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return append([]lipapi.Event(nil), p.seenEvents...)
}

func (p *responsePipeline) clearClientAccumulators() {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	p.ensure()
	p.seenEvents = nil
	p.visibleText.Reset()
	if p.customer != nil {
		p.customer.resetContent()
	}
}

// clearToolClassification drops correlation for an attempt transition or
// terminal cleanup. The logical committed-tool evidence remains available for
// the successful-turn keep-warm handoff.
func (p *responsePipeline) clearToolClassification() {
	if p == nil {
		return
	}
	p.toolClass.clear()
}

func clearAttemptToolState(p *responsePipeline, attempt *attemptSession) {
	if attempt != nil && attempt.toolFinal != nil {
		attempt.toolFinal.clear()
	}
	if p != nil {
		p.clearToolClassification()
	}
}

func (p *responsePipeline) enrichToolEvent(te *lipapi.ToolEvent) {
	if p != nil {
		p.toolClass.enrich(te)
	}
}

func (p *responsePipeline) forgetToolClassification(id string) {
	if p != nil {
		p.toolClass.forget(id)
	}
}

func (p *responsePipeline) observeToolFinalName(id string, ev lipapi.Event) {
	if p != nil {
		p.toolClass.observeFinalName(id, ev)
	}
}

func (p *responsePipeline) rememberEffectiveTool(id string, ev lipapi.ToolEvent) {
	if p != nil {
		p.toolClass.rememberEffective(id, ev)
	}
}

func (p *responsePipeline) committedToolEventsSnapshot() []lipapi.ToolEvent {
	if p == nil {
		return nil
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return append([]lipapi.ToolEvent(nil), p.committedTools...)
}

func (p *responsePipeline) recordingBlocksReplacement() bool {
	if p == nil {
		return false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return p.recordingOutcome == responseRecordingMandatoryPostCommitFailure
}

func (p *responsePipeline) setRecordingOutcome(outcome responseRecordingOutcome) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.recordingOutcome = outcome
	p.eventsMu.Unlock()
}

// resetForReplacement clears evidence whose lifetime is one B-leg. Gate and
// recovery queues intentionally remain untouched; their ordering spans the
// logical response even when the backend attempt changes.
func (p *responsePipeline) resetForReplacement() {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.ensure()
	p.seenEvents = nil
	p.visibleText.Reset()
	p.lastAuthorityUsage = lipapi.Event{}
	p.lastCustomerUsage = lipapi.Event{}
	if p.customer != nil {
		p.customer.resetContent()
	}
	p.eventsMu.Unlock()
}

func (p *responsePipeline) releasedOutputText() string {
	if p == nil {
		return ""
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	p.ensure()
	if p.customer != nil {
		text, _, _, _ := p.customer.Snapshot()
		return text
	}
	return p.visibleText.String()
}

// resolveCustomerUsage prefers a reconstruction from released customer
// content, then the last synthesized customer event, and finally a
// provider-stripped shell. Provider/operator evidence is never imported into
// the customer plane except for that empty shell.
func (p *responsePipeline) resolveCustomerUsage(ctx context.Context, usageEv lipapi.Event) lipapi.Event {
	if p == nil {
		return customerPlaneUsageEvent(usageEv)
	}
	p.eventsMu.Lock()
	reconstructor := p.customerUsageFn
	last := p.lastCustomerUsage
	p.eventsMu.Unlock()
	if reconstructor != nil {
		if ev := reconstructor(ctx, p.releasedOutputText(), p.contentEvents()); ev.Kind != "" {
			return ev
		}
	}
	if last.Kind != "" {
		return last
	}
	out := customerPlaneUsageEvent(usageEv)
	if out.Kind == "" && usageEv.Kind != "" {
		return lipapi.Event{Kind: lipapi.EventUsageDelta}
	}
	return out
}

func (p *responsePipeline) contentEvents() []lipapi.Event {
	if p == nil || p.customer == nil {
		return nil
	}
	return p.customer.contentEvents()
}

func (p *responsePipeline) markCustomerSettled() bool {
	if p == nil {
		return true
	}
	p.eventsMu.Lock()
	p.ensure()
	customer := p.customer
	p.eventsMu.Unlock()
	return customer.MarkSettled()
}

func (p *responsePipeline) unmarkCustomerSettled() {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.ensure()
	customer := p.customer
	p.eventsMu.Unlock()
	customer.unmarkSettled()
}

func (p *responsePipeline) setLastAuthorityUsage(ev lipapi.Event) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.lastAuthorityUsage = ev
	p.eventsMu.Unlock()
}

func (p *responsePipeline) lastAuthorityUsageSnapshot() lipapi.Event {
	if p == nil {
		return lipapi.Event{}
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return p.lastAuthorityUsage
}

func (p *responsePipeline) setLastCustomerUsage(ev lipapi.Event) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.lastCustomerUsage = ev
	p.eventsMu.Unlock()
}

func (p *responsePipeline) lastCustomerUsageSnapshot() lipapi.Event {
	if p == nil {
		return lipapi.Event{}
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return p.lastCustomerUsage
}

func (p *responsePipeline) operatorUsageForFinalize() lipapi.Event {
	if p == nil {
		return emptyOperatorUsageShell()
	}
	if usage := p.lastAuthorityUsageSnapshot(); usage.Kind != "" {
		return usage
	}
	return operatorUsageOrShell(p.seenEventsCopy())
}

func (p *responsePipeline) usageEvidenceOrEmpty() lipapi.Event {
	if p == nil {
		return emptyOperatorUsageShell()
	}
	if usage := authorityUsageEvent(tokenAccountingUsageEvents(p.seenEventsCopy())); usage.Kind != "" {
		return usage
	}
	if usage := p.lastAuthorityUsageSnapshot(); usage.Kind != "" {
		return usage
	}
	return emptyOperatorUsageShell()
}

func (p *responsePipeline) billingEvidenceFallback() lipapi.Event {
	if p == nil {
		return emptyOperatorUsageShell()
	}
	if usage := p.lastAuthorityUsageSnapshot(); usage.Kind != "" {
		return usage
	}
	return lastUsageDeltaOrShell(p.seenEventsCopy())
}

type responseGateInput struct {
	meta      completion.Meta
	services  completion.Services
	stageLog  *slog.Logger
	committed bool
	limits    completion.BufferLimits
}

// completionGatedEmit owns gate buffers and drain state. It returns a gate
// replacement marker to the caller; response/terminal side effects remain at
// the caller boundary and are not owned by this pipeline.
func (p *responsePipeline) completionGatedEmit(
	ctx context.Context,
	gates []completion.Gate,
	ev lipapi.Event,
	in responseGateInput,
) (lipapi.Event, bool, error) {
	if p == nil {
		return lipapi.Event{}, false, errors.New("runtime: nil response pipeline")
	}
	p.eventsMu.Lock()
	p.ensure()
	if p.gateLive {
		p.eventsMu.Unlock()
		return ev, false, nil
	}
	limits := in.limits
	if limits.MaxEvents <= 0 {
		limits = completion.DefaultBufferLimits()
	}
	if len(p.gateBuf) == 0 {
		maxEv := limits.MaxEvents
		if maxEv <= 0 {
			maxEv = completion.DefaultBufferLimits().MaxEvents
		}
		const prealloc = 64
		p.gateBuf = make([]lipapi.Event, 0, min(prealloc, maxEv))
	}
	p.gateBuf = append(p.gateBuf, ev)
	if extensions.CompletionGateBufferExceeded(limits, len(p.gateBuf)) {
		p.gateLive = true
		p.gateDrain = slices.Clone(p.gateBuf)
		p.gateBuf = nil
		if len(p.gateDrain) == 0 {
			p.eventsMu.Unlock()
			return lipapi.Event{}, false, errors.New("runtime: completion gate overflow with empty buffer")
		}
		first := p.gateDrain[0]
		p.gateDrain = p.gateDrain[1:]
		p.eventsMu.Unlock()
		return first, false, nil
	}
	if ev.Kind != lipapi.EventResponseFinished {
		p.eventsMu.Unlock()
		return lipapi.Event{}, false, errGateContinueInner
	}
	buffered := slices.Clone(p.gateBuf)
	p.eventsMu.Unlock()

	committedForPanic := in.committed || gateBufHasCommittedOutput(buffered)
	gateResult, err := safety.CallValue(safety.BoundaryStream, "completion_gate_chain", func() (extensions.CompletionGateChainResult, error) {
		return extensions.ApplyCompletionGateChain(ctx, gates, in.meta, buffered, in.committed, in.services, in.stageLog)
	})
	if err != nil {
		p.eventsMu.Lock()
		p.gateBuf = nil
		var pe *safety.PanicError
		if errors.As(err, &pe) {
			p.gateDrain = nil
			p.gateLive = false
		}
		p.eventsMu.Unlock()
		if errors.As(err, &pe) {
			return lipapi.Event{}, false, mapStreamPanic(pe, committedForPanic)
		}
		return lipapi.Event{}, false, err
	}
	out := gateResult.Events
	if len(out) == 0 {
		p.eventsMu.Lock()
		p.gateBuf = nil
		p.eventsMu.Unlock()
		return lipapi.Event{}, false, errors.New("runtime: completion gate produced empty stream")
	}
	p.eventsMu.Lock()
	p.gateBuf = nil
	p.gateDrain = slices.Clone(out[1:])
	p.eventsMu.Unlock()
	return out[0], gateResult.Replaced, nil
}

func (p *responsePipeline) popGateDrainHead() (lipapi.Event, bool) {
	if p == nil {
		return lipapi.Event{}, false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if len(p.gateDrain) == 0 {
		return lipapi.Event{}, false
	}
	ev := p.gateDrain[0]
	p.gateDrain = p.gateDrain[1:]
	return ev, true
}

type accumulatorSnapWire struct {
	Events int    `json:"e"`
	Text   string `json:"t,omitempty"`
	Final  bool   `json:"f"`
}

func (p *responsePipeline) accumulatorSnapshot(flags ...bool) coreterm.AccumulatorSnapshot {
	if p == nil {
		return coreterm.NewAccumulatorSnapshot(nil, false)
	}
	committed := false
	finalized := false
	if len(flags) > 0 {
		committed = flags[0]
	}
	if len(flags) > 1 {
		finalized = flags[1]
	}
	if len(flags) == 0 {
		p.eventsMu.Lock()
		terminalSnapshot := p.terminalSnapshot
		p.eventsMu.Unlock()
		if terminalSnapshot != nil {
			// Read the terminal owner's flags before entering the response snapshot
			// boundary. No response lock may be held across an owner callback.
			committed, finalized = terminalSnapshot()
		}
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	p.ensure()
	w := accumulatorSnapWire{Events: len(p.seenEvents)}
	w.Final = finalized
	if p.customer != nil {
		w.Text, _, _, _ = p.customer.Snapshot()
	} else {
		w.Text = p.visibleText.String()
	}
	raw, _ := json.Marshal(w)
	return coreterm.NewAccumulatorSnapshot(raw, committed)
}

func (p *responsePipeline) setGateDrain(events []lipapi.Event) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.gateDrain = slices.Clone(events)
	p.eventsMu.Unlock()
}

func (p *responsePipeline) popGateDrain() (lipapi.Event, bool) {
	return p.popGateDrainHead()
}

func (p *responsePipeline) abandonIncompleteGateBuffer() {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if !p.gateLive && len(p.gateBuf) > 0 && !extensions.StreamFinished(p.gateBuf) {
		p.gateBuf = nil
	}
}

func (p *responsePipeline) setRecoveryDrain(events []lipapi.Event) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.recoverDrain = slices.Clone(events)
	p.eventsMu.Unlock()
}

func (p *responsePipeline) prependRecoveryDrain(events ...lipapi.Event) {
	if p == nil || len(events) == 0 {
		return
	}
	p.eventsMu.Lock()
	p.recoverDrain = append(slices.Clone(events), p.recoverDrain...)
	p.eventsMu.Unlock()
}

func (p *responsePipeline) appendRecoveryDrain(events ...lipapi.Event) {
	if p == nil || len(events) == 0 {
		return
	}
	p.eventsMu.Lock()
	p.recoverDrain = append(p.recoverDrain, events...)
	p.eventsMu.Unlock()
}

func (p *responsePipeline) popRecoveryDrain() (lipapi.Event, bool) {
	if p == nil {
		return lipapi.Event{}, false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if len(p.recoverDrain) == 0 {
		return lipapi.Event{}, false
	}
	ev := p.recoverDrain[0]
	p.recoverDrain = p.recoverDrain[1:]
	return ev, true
}
