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

	// mandatorySafe is the document a finalizer that published a declared mandatory
	// completeness requirement was shown, and returned a usable result for. It is
	// retained so a LATER finalizer's unusable result can never fall back on the
	// original fragments over a decision that was already made on it.
	//
	// It is nil until such a requirement has actually been satisfied for this call,
	// so the retention is exactly ONE bounded reference per active tool call and
	// never a growing store: it is written at most once, by the declaring finalizer,
	// and it dies with the buffer. See [toolCallAssembler.unusableResult] for the
	// decision it drives.
	mandatorySafe *toolCallDocument
}

// toolCallDocument is one complete tool-call document: the arguments the assembler
// is carrying for a call plus the tool name they belong to.
type toolCallDocument struct {
	name string
	args []byte
	// rewrote records whether this document already differs from the original
	// fragments, which is exactly the condition under which the release synthesizes
	// a new canonical lifecycle instead of replaying those fragments unchanged.
	rewrote bool
}

// retainMandatorySafe records the document the assembler is carrying as this call's
// mandatory-safe result.
//
// It is called only on the declaring finalizer's own usable result, so it is called
// at most once per call. The arguments it stores are the private copy the assembler
// already hands to the next finalizer, so nothing is copied here and nothing the
// stored slice aliases can still be mutated: every later rewrite REPLACES the
// assembler's arguments with a fresh copy rather than editing them in place.
func (b *toolCallBuffer) retainMandatorySafe(name string, args []byte, rewrote bool) {
	b.mandatorySafe = &toolCallDocument{name: name, args: args, rewrote: rewrote}
}

// mandatorySafeRelease is the release lifecycle for this call's retained
// mandatory-safe result: the synthesized canonical lifecycle when the document
// already differs from the original fragments, and the untouched fragments when it
// does not.
//
// The second branch is byte-identical to the pre-existing replay, which is what
// keeps a call whose declaring finalizer changed nothing - or a call with no
// earlier rewrite in front of it - behaving precisely as it did before, including
// the fragment boundaries a client already saw.
func (b *toolCallBuffer) mandatorySafeRelease() []lipapi.Event {
	if b.mandatorySafe == nil {
		return nil
	}
	if b.mandatorySafe.rewrote {
		return synthesizeRewriteLifecycle(b, b.mandatorySafe.name, b.mandatorySafe.args)
	}
	return slices.Clone(b.originals)
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

	// Whatever finalizeCall decided to release is queued BEFORE its error is
	// returned, so the assembler never silently discards a document it decided to
	// keep in favour of the originals. A refusal returns no events at all, so this
	// changes nothing for the closed-answer cases; only the preserved
	// mandatory-safe result is both released and failed, and what the client ends
	// up observing for that turn stays the pre-existing terminal error path's
	// decision rather than the assembler's.
	emit, err := a.finalizeCall(ctx, buf, meta)
	a.enqueue(emit...)
	if err != nil {
		return true, err
	}
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
	// soon as the declaring finalizer is invoked; from that point the requirement
	// is satisfied by that finalizer's own usable result and the decision belongs
	// to [toolCallAssembler.unusableResult].
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
		// declares is this finalizer's own capability answer, read once and used for
		// both halves of the requirement: clearing the pending flag before it runs,
		// and retaining its result as the call's mandatory-safe document afterwards.
		declares := declaresMandatoryBound(fin)
		if mandatoryPending && declares {
			mandatoryPending = false
		}
		op := "tool_call_finalizer:" + fin.ID()
		res, err := safety.CallValue(safety.BoundaryExtension, op, func() (toolcall.Result, error) {
			return fin.Finalize(ctx, call, tool, catalogCopy, meta)
		})
		if err != nil {
			return a.unusableResult(buf, mandatoryPending, err)
		}
		switch res.Action {
		case toolcall.ActionPass:
			if declares {
				// The declaring finalizer saw these arguments and accepted them, so
				// they are this call's mandatory-safe document whatever happens to a
				// later finalizer.
				buf.retainMandatorySafe(name, args, rewrote)
			}
			continue
		case toolcall.ActionReject:
			return nil, &toolcall.RejectError{ReasonCode: res.ReasonCode, ToolCallID: buf.id}
		case toolcall.ActionRewrite:
			if !rewriteEnvelopeValid(res) {
				// No error: the finalizer returned a well-formed Result the assembler
				// cannot use, so there is no failure of its own to surface.
				return a.unusableResult(buf, mandatoryPending, nil)
			}
			name = strings.TrimSpace(res.ToolName)
			args = append([]byte(nil), res.ArgsJSON...)
			rewrote = true
			if declares {
				buf.retainMandatorySafe(name, args, true)
			}
		default:
			// Likewise for an action outside the closed vocabulary: unusable, but not
			// reported as an error by the finalizer that produced it.
			return a.unusableResult(buf, mandatoryPending, nil)
		}
	}
	if !rewrote {
		return slices.Clone(buf.originals), nil
	}
	return synthesizeRewriteLifecycle(buf, name, args), nil
}

// unusableResult is the assembler's single fallback for a finalizer that failed,
// panicked, or returned a result it cannot use. It has exactly three answers, and
// the pre-existing replay is the last of them:
//
//  1. an UNDECIDED mandatory completeness requirement refuses closed. The declaring
//     finalizer has not decided on these arguments, so releasing them is exactly
//     the bypass the capability exists to prevent (requirement 4.6);
//  2. a SATISFIED requirement releases the document the declaring finalizer was
//     shown and accepted, PRESERVING the mandatory-safe result rather than
//     discarding it and replaying the original - possibly alias-bearing - fragments
//     over it. cause is the later finalizer's own failure and still surfaces;
//  3. anything else - no declared requirement, or a declaring finalizer that itself
//     failed and therefore produced no decision to preserve - replays the original
//     fragments unchanged. For a call with no declared requirement that is the
//     pre-existing assembler behavior, byte for byte.
//
// WHY THIS IS NOT AN ORDERING RULE. It reads no finalizer's position, reorders
// nothing, and changes nothing about how the chain is materialized: answer 2 is
// decided entirely from the document a declaring finalizer produced, so it holds
// for a later finalizer at any order above the declaring one, and answer 3 is
// unchanged for every call that published no such requirement. Refusing instead of
// preserving was considered and is worse: it converts a correctly expanded call
// into a hard client reject because of a failure that has nothing to do with the
// decision already made.
func (a *toolCallAssembler) unusableResult(buf *toolCallBuffer, mandatoryPending bool, cause error) ([]lipapi.Event, error) {
	if mandatoryPending {
		return nil, a.refusalIncomplete(buf.id)
	}
	if buf.mandatorySafe == nil {
		return slices.Clone(buf.originals), nil
	}
	return buf.mandatorySafeRelease(), cause
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
