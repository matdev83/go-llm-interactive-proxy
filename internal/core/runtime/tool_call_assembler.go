package runtime

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

// Kept equal to toolcallrepair repair.DefaultMaxArgsBytes by
// TestDefaultToolCallFinalizationMaxArgsBytesMatchCore.
const defaultToolCallFinalizationMaxArgsBytes = 64 * 1024

type toolCallBuffer struct {
	id           string
	name         string
	messageIndex int
	originals    []lipapi.Event
	args         []byte
}

// toolCallAssembler is owned by a single retryRecvStream and driven only from
// that stream's Recv loop (no concurrent access).
type toolCallAssembler struct {
	finalizers   []toolcall.Finalizer
	maxArgsBytes int
	catalog      []lipapi.ToolDef

	// mandatory is the projection of every finalizer that publishes the
	// optional toolcall.BufferingRequirement capability. It owns the effective
	// assembly bound and the refusal policy; maxArgsBytes keeps its pre-existing
	// meaning as the clamped legacy shared bound, which is the FLOOR of the
	// effective assembly bound, not something any finalizer reads here.
	mandatory mandatoryBuffering

	active      map[string]*toolCallBuffer
	passThrough map[string]struct{}
	completed   map[string]struct{}
	// refusing holds tool calls already refused closed past the effective
	// assembly bound. Every later fragment is held and dropped so that no
	// possibly alias-bearing argument is released, and only the finished event
	// produces the typed refusal.
	refusing map[string]struct{}
	drain    []lipapi.Event
}

func newToolCallAssembler(finalizers []toolcall.Finalizer, maxArgsBytes int, catalog []lipapi.ToolDef) *toolCallAssembler {
	fs := toolcall.MaterializeSorted(finalizers)
	if len(fs) == 0 || len(catalog) == 0 {
		return nil
	}
	maxArgsBytes = clampToolCallFinalizationMaxArgsBytes(maxArgsBytes)
	return &toolCallAssembler{
		finalizers:   fs,
		maxArgsBytes: maxArgsBytes,
		catalog:      cloneToolCatalog(catalog),
		mandatory:    resolveMandatoryBuffering(fs, maxArgsBytes),
		active:       make(map[string]*toolCallBuffer),
		passThrough:  make(map[string]struct{}),
		completed:    make(map[string]struct{}),
		refusing:     make(map[string]struct{}),
	}
}

func clampToolCallFinalizationMaxArgsBytes(maxArgsBytes int) int {
	if maxArgsBytes <= 0 {
		return defaultToolCallFinalizationMaxArgsBytes
	}
	if maxArgsBytes > lipapi.MaxEventDeltaBytes {
		return lipapi.MaxEventDeltaBytes
	}
	return maxArgsBytes
}

func (a *toolCallAssembler) enabled() bool {
	return a != nil && len(a.finalizers) > 0 && len(a.catalog) > 0
}

func (a *toolCallAssembler) clear() {
	if a == nil {
		return
	}
	a.active = make(map[string]*toolCallBuffer)
	a.passThrough = make(map[string]struct{})
	a.completed = make(map[string]struct{})
	a.refusing = make(map[string]struct{})
	a.drain = nil
}

func (a *toolCallAssembler) popDrain() (lipapi.Event, bool) {
	if a == nil {
		return lipapi.Event{}, false
	}
	if len(a.drain) == 0 {
		return lipapi.Event{}, false
	}
	ev := a.drain[0]
	a.drain[0] = lipapi.Event{} // drop large Delta references from the backing array
	a.drain = a.drain[1:]
	if len(a.drain) == 0 {
		a.drain = nil
	}
	return ev, true
}

func (a *toolCallAssembler) enqueue(evs ...lipapi.Event) {
	if a == nil || len(evs) == 0 {
		return
	}
	a.drain = append(a.drain, evs...)
}

// ingest handles one backend event after BTP. held=true means the event must not
// continue on the normal tool path; any finalized lifecycle is queued on drain.
func (a *toolCallAssembler) ingest(ctx context.Context, ev lipapi.Event, meta toolcall.Meta) (held bool, err error) {
	if !a.enabled() {
		return false, nil
	}
	switch ev.Kind {
	case lipapi.EventToolCallStarted, lipapi.EventToolCallArgsDelta, lipapi.EventToolCallFinished:
	default:
		return false, nil
	}
	id := strings.TrimSpace(ev.ToolCallID)
	if id == "" {
		return false, nil
	}

	if _, ok := a.passThrough[id]; ok {
		return false, nil
	}
	if _, ok := a.refusing[id]; ok {
		return a.ingestRefused(ev, id)
	}

	switch ev.Kind {
	case lipapi.EventToolCallStarted:
		return a.ingestStarted(ev, id), nil
	case lipapi.EventToolCallArgsDelta:
		return a.ingestDelta(ev, id), nil
	case lipapi.EventToolCallFinished:
		return a.ingestFinished(ctx, ev, id, meta)
	default:
		return false, nil
	}
}

func (a *toolCallAssembler) ingestStarted(ev lipapi.Event, id string) bool {
	if _, done := a.completed[id]; done {
		a.passThrough[id] = struct{}{}
		return false
	}
	if buf, ok := a.active[id]; ok {
		a.enqueue(slices.Clone(buf.originals)...)
		a.enqueue(ev)
		delete(a.active, id)
		a.passThrough[id] = struct{}{}
		return true
	}
	a.active[id] = &toolCallBuffer{
		id:           id,
		name:         ev.ToolName,
		messageIndex: ev.MessageIndex,
		originals:    []lipapi.Event{ev},
	}
	return true
}

// ingestRefused keeps a tool call that is already refused closed entirely away
// from the client: every further fragment is held and dropped, and only the
// finished event yields the typed refusal. Nothing alias-bearing can therefore
// be released on a call whose mandatory requirement was not honored.
func (a *toolCallAssembler) ingestRefused(ev lipapi.Event, id string) (bool, error) {
	if ev.Kind != lipapi.EventToolCallFinished {
		return true, nil
	}
	delete(a.refusing, id)
	a.completed[id] = struct{}{}
	return true, a.refusal(id)
}

func (a *toolCallAssembler) ingestDelta(ev lipapi.Event, id string) bool {
	buf, ok := a.active[id]
	if !ok {
		a.passThrough[id] = struct{}{}
		return false
	}
	delta := ev.Delta
	// Overflow-safe: never add len(buf.args)+len(delta) (can wrap on extreme caps).
	limit := a.mandatory.assemblyMaxArgsBytes
	if len(buf.args) > limit || len(delta) > limit-len(buf.args) {
		// A mandatory finalizer must never see the fragments replayed unchanged
		// past its own bound, and an unusable declaration can never be honored at
		// any bound. Refuse the call closed: the buffer is dropped without being
		// released, and the typed refusal is deferred to the finished event so no
		// alias-bearing argument reaches the client in the meantime.
		if a.mandatory.failClosedPastBound() {
			delete(a.active, id)
			a.refusing[id] = struct{}{}
			return true
		}
		buf.originals = append(buf.originals, ev)
		a.enqueue(slices.Clone(buf.originals)...)
		delete(a.active, id)
		a.passThrough[id] = struct{}{}
		return true
	}
	buf.originals = append(buf.originals, ev)
	buf.args = append(buf.args, delta...)
	return true
}

func (a *toolCallAssembler) ingestFinished(ctx context.Context, ev lipapi.Event, id string, meta toolcall.Meta) (bool, error) {
	buf, ok := a.active[id]
	if !ok {
		a.passThrough[id] = struct{}{}
		return false, nil
	}
	buf.originals = append(buf.originals, ev)
	delete(a.active, id)
	a.completed[id] = struct{}{}

	emit, err := a.finalizeCall(ctx, buf, meta)
	if err != nil {
		return true, err
	}
	a.enqueue(emit...)
	return true, nil
}

func (a *toolCallAssembler) finalizeCall(ctx context.Context, buf *toolCallBuffer, meta toolcall.Meta) ([]lipapi.Event, error) {
	// A finalizer that published a present-but-unusable mandatory declaration
	// cannot be honored at any bound, and treating it as "did not opt in" would
	// downgrade a mandatory completeness requirement into optional pass-through
	// handling. Refuse this call closed before any finalizer runs and before any
	// original fragment is released.
	if a.mandatory.invalidDeclaration {
		return nil, a.refusal(buf.id)
	}

	name := buf.name
	args := append([]byte(nil), buf.args...)
	rewrote := false
	// mandatoryPending records that a well-formed mandatory completeness
	// requirement applies to this call and the finalizer that declared it has
	// not been invoked yet. Requirement 4.6's failure clause: an unrelated
	// finalizer that fails first must not silently skip it, because replaying the
	// original fragments is exactly what releases possibly alias-bearing
	// arguments no declaring finalizer ever decided on. The flag is cleared as
	// soon as the declaring finalizer is invoked, so from that point its own
	// error keeps the pre-existing replay semantics.
	mandatoryPending := a.mandatory.mandatoryBoundDeclared

	for _, fin := range a.finalizers {
		if fin == nil {
			continue
		}
		// Defensive catalog copy per Finalize (ADR); assembler catalog is owned.
		catalogCopy := cloneToolCatalog(a.catalog)
		tool := lookupToolDef(catalogCopy, name)
		call := toolcall.CompletedCall{
			ToolCallID: buf.id,
			ToolName:   name,
			ArgsJSON:   append([]byte(nil), args...),
		}
		if mandatoryPending && declaresMandatoryBound(fin) {
			mandatoryPending = false
		}
		op := "tool_call_finalizer:" + fin.ID()
		res, err := safety.CallValue(safety.BoundaryExtension, op, func() (toolcall.Result, error) {
			return fin.Finalize(ctx, call, tool, catalogCopy, meta)
		})
		if err != nil {
			return a.undecidedMandatoryReplay(buf, mandatoryPending)
		}
		switch res.Action {
		case toolcall.ActionPass:
			continue
		case toolcall.ActionReject:
			return nil, &toolcall.RejectError{ReasonCode: res.ReasonCode, ToolCallID: buf.id}
		case toolcall.ActionRewrite:
			if !rewriteEnvelopeValid(res) {
				return a.undecidedMandatoryReplay(buf, mandatoryPending)
			}
			name = strings.TrimSpace(res.ToolName)
			args = append([]byte(nil), res.ArgsJSON...)
			rewrote = true
		default:
			return a.undecidedMandatoryReplay(buf, mandatoryPending)
		}
	}
	if !rewrote {
		return slices.Clone(buf.originals), nil
	}
	return synthesizeRewriteLifecycle(buf, name, args), nil
}

// undecidedMandatoryReplay is the assembler fallback for an ordinary finalizer
// that failed or produced an unusable result: pre-existing behavior replays the
// original fragments unchanged, but that silently skips path expansion whenever
// a mandatory completeness requirement is still undecided (requirement 4.6), so
// the call is refused closed instead.
//
// This does not reorder finalizers and does not change the fallback for any
// finalizer without a declared requirement.
func (a *toolCallAssembler) undecidedMandatoryReplay(buf *toolCallBuffer, mandatoryPending bool) ([]lipapi.Event, error) {
	if mandatoryPending {
		return nil, a.refusalIncomplete(buf.id)
	}
	return slices.Clone(buf.originals), nil
}

func cloneToolCatalog(catalog []lipapi.ToolDef) []lipapi.ToolDef {
	if len(catalog) == 0 {
		return nil
	}
	out := make([]lipapi.ToolDef, len(catalog))
	for i, t := range catalog {
		out[i] = t
		if t.Parameters != nil {
			out[i].Parameters = append([]byte(nil), t.Parameters...)
		}
	}
	return out
}

func rewriteEnvelopeValid(res toolcall.Result) bool {
	return strings.TrimSpace(res.ToolName) != "" && res.ArgsJSON != nil && json.Valid(res.ArgsJSON)
}

func synthesizeRewriteLifecycle(buf *toolCallBuffer, name string, args []byte) []lipapi.Event {
	return []lipapi.Event{
		{
			Kind:         lipapi.EventToolCallStarted,
			ToolCallID:   buf.id,
			ToolName:     name,
			MessageIndex: buf.messageIndex,
		},
		{
			Kind:         lipapi.EventToolCallArgsDelta,
			ToolCallID:   buf.id,
			ToolName:     name,
			Delta:        string(args),
			MessageIndex: buf.messageIndex,
		},
		{
			Kind:         lipapi.EventToolCallFinished,
			ToolCallID:   buf.id,
			ToolName:     name,
			MessageIndex: buf.messageIndex,
		},
	}
}

func lookupToolDef(catalog []lipapi.ToolDef, name string) lipapi.ToolDef {
	for _, t := range catalog {
		if t.Name == name {
			return t
		}
	}
	return lipapi.ToolDef{}
}
